package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

// Zabbix bounds a maintenance period to between five minutes and roughly a
// thousand days, and rounds every timestamp down to the minute.
const (
	minMaintenancePeriod = 300 * time.Second
	maxMaintenancePeriod = 86399940 * time.Second
)

// Maintenance types.
const (
	MaintenanceWithData    = "0"
	MaintenanceWithoutData = "1"
)

// Maintenance is the compact projection of a maintenance window.
type Maintenance struct {
	ID          string `json:"maintenanceid"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Collecting  bool   `json:"collecting_data"`
	ActiveSince string `json:"active_since"`
	ActiveTill  string `json:"active_till"`
	Active      bool   `json:"active"`
	Expired     bool   `json:"expired"`
	// EndsIn is empty for an expired window.
	EndsIn string        `json:"ends_in,omitempty"`
	Hosts  []ProblemHost `json:"hosts,omitempty"`
	Groups []string      `json:"groups,omitempty"`
}

type wireMaintenance struct {
	MaintenanceID   string `json:"maintenanceid"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	MaintenanceType string `json:"maintenance_type"`
	ActiveSince     string `json:"active_since"`
	ActiveTill      string `json:"active_till"`
	Hosts           []struct {
		HostID string `json:"hostid"`
		Name   string `json:"name"`
	} `json:"hosts"`
	HostGroups []struct {
		GroupID string `json:"groupid"`
		Name    string `json:"name"`
	} `json:"hostgroups"`
	TimePeriods []struct {
		TimePeriodType string `json:"timeperiod_type"`
		StartDate      string `json:"start_date"`
		Period         string `json:"period"`
	} `json:"timeperiods"`
}

// MaintenanceQuery selects maintenance windows.
type MaintenanceQuery struct {
	HostID string
	Search string
	Limit  int
	// IncludeExpired keeps windows whose active period has passed. They are
	// shown by default, because "the window already ended, remove it" is a
	// routine follow-up.
	ExcludeExpired bool
}

// ListMaintenance returns maintenance windows.
func (s *Service) ListMaintenance(ctx context.Context, q MaintenanceQuery) ([]Maintenance, bool, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	params := map[string]any{
		"output":            "extend",
		"selectHosts":       []string{"hostid", "name"},
		"selectHostGroups":  []string{"groupid", "name"},
		"selectTimeperiods": "extend",
		"sortfield":         "active_since",
		"sortorder":         "DESC",
		"limit":             limit + 1,
	}
	if q.HostID != "" {
		params["hostids"] = []string{q.HostID}
	}
	applySearch(params, q.Search, "name")
	var wire []wireMaintenance
	if err := s.client.CallIdempotent(ctx, "maintenance.get", params, &wire); err != nil {
		return nil, false, errs.FromAPI(err)
	}
	now := time.Now()
	out := make([]Maintenance, 0, len(wire))
	for _, w := range wire {
		m := w.toMaintenance(now)
		if q.ExcludeExpired && m.Expired {
			continue
		}
		out = append(out, m)
	}
	kept, truncated := output.Bound(out, limit)
	return kept, truncated, nil
}

func (w wireMaintenance) toMaintenance(now time.Time) Maintenance {
	since := unixToTime(w.ActiveSince)
	till := unixToTime(w.ActiveTill)
	m := Maintenance{
		ID:          w.MaintenanceID,
		Name:        output.Sanitise(w.Name),
		Description: output.Sanitise(w.Description),
		Collecting:  w.MaintenanceType == MaintenanceWithData,
		ActiveSince: rfc3339(since),
		ActiveTill:  rfc3339(till),
	}
	m.Expired = !till.IsZero() && till.Before(now)
	m.Active = !m.Expired && !since.IsZero() && !since.After(now)
	if !m.Expired && !till.IsZero() {
		m.EndsIn = HumanDuration(till.Sub(now))
	}
	for _, h := range w.Hosts {
		m.Hosts = append(m.Hosts, ProblemHost{ID: h.HostID, Name: output.Sanitise(h.Name)})
	}
	for _, g := range w.HostGroups {
		m.Groups = append(m.Groups, output.Sanitise(g.Name))
	}
	return m
}

// GetMaintenance returns one window by identifier.
func (s *Service) GetMaintenance(ctx context.Context, id string) (*Maintenance, error) {
	params := map[string]any{
		"output":            "extend",
		"maintenanceids":    []string{id},
		"selectHosts":       []string{"hostid", "name"},
		"selectHostGroups":  []string{"groupid", "name"},
		"selectTimeperiods": "extend",
	}
	var wire []wireMaintenance
	if err := s.client.CallIdempotent(ctx, "maintenance.get", params, &wire); err != nil {
		return nil, errs.FromAPI(err)
	}
	if len(wire) == 0 {
		return nil, errs.NotFound("no maintenance window has ID %s", id)
	}
	m := wire[0].toMaintenance(time.Now())
	return &m, nil
}

// MaintenanceCreateRequest describes a window to open.
type MaintenanceCreateRequest struct {
	Name string
	// Hosts holds names or patterns. A pattern such as "ms*" expands here, so
	// that an operator can silence a fleet the way they describe it.
	Hosts    []string
	Groups   []string
	Duration time.Duration
	StartAt  time.Time
	// CollectData keeps Zabbix polling during the window. Off means the hosts
	// go dark, which also hides a real outage that starts during it.
	CollectData bool
	Description string
}

// PlanMaintenanceCreate resolves every name and returns the change without
// making it.
func (s *Service) PlanMaintenanceCreate(ctx context.Context, profile string, req MaintenanceCreateRequest) (*safety.Plan, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errs.Usage("a maintenance window needs a name")
	}
	if len(req.Hosts) == 0 && len(req.Groups) == 0 {
		return nil, errs.Usage("name at least one host or host group")
	}
	if req.Duration < minMaintenancePeriod {
		return nil, errs.Usage("a maintenance window must last at least %s", HumanDuration(minMaintenancePeriod))
	}
	if req.Duration > maxMaintenancePeriod {
		return nil, errs.Usage("a maintenance window may not exceed %s", HumanDuration(maxMaintenancePeriod))
	}
	start := req.StartAt
	if start.IsZero() {
		start = time.Now()
	}
	// Zabbix rounds timestamps down to the minute; doing it here keeps the
	// plan honest about what will actually be created.
	start = start.Truncate(time.Minute)
	end := start.Add(req.Duration).Truncate(time.Minute)

	hosts, err := s.expandHostPatterns(ctx, req.Hosts)
	if err != nil {
		return nil, err
	}
	groups, err := s.expandGroupPatterns(ctx, req.Groups)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 && len(groups) == 0 {
		return nil, errs.NotFound("no host or host group matched %s",
			strings.Join(append(append([]string{}, req.Hosts...), req.Groups...), ", "))
	}

	plan, err := safety.NewPlan("maintenance.create", profile, safety.RiskWrite, "maintenance")
	if err != nil {
		return nil, err
	}
	// Zabbix requires the host and group entries to carry nothing but their
	// identifier. Passing a bare hostids array, as several older wrappers do,
	// is rejected outright.
	hostRefs := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		hostRefs = append(hostRefs, map[string]any{"hostid": h.ID})
	}
	groupRefs := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		groupRefs = append(groupRefs, map[string]any{"groupid": g.ID})
	}
	maintType := MaintenanceWithData
	if !req.CollectData {
		maintType = MaintenanceWithoutData
	}
	params := map[string]any{
		"name":             req.Name,
		"active_since":     start.Unix(),
		"active_till":      end.Unix(),
		"maintenance_type": maintType,
		"timeperiods": []map[string]any{{
			"timeperiod_type": 0, // one time only
			"start_date":      start.Unix(),
			"period":          int64(end.Sub(start).Seconds()),
		}},
	}
	if req.Description != "" {
		params["description"] = req.Description
	}
	if len(hostRefs) > 0 {
		params["hosts"] = hostRefs
	}
	if len(groupRefs) > 0 {
		params["groups"] = groupRefs
	}
	plan.Params = params
	plan.ImpactCount = len(hosts) + len(groups)
	plan.Summary = fmt.Sprintf("Create maintenance %q for %s, %s to %s",
		req.Name, HumanDuration(end.Sub(start)),
		start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	for _, h := range hosts {
		plan.Resources = append(plan.Resources, safety.Resource{Kind: "host", ID: h.ID, Name: h.Name})
	}
	for _, g := range groups {
		plan.Resources = append(plan.Resources, safety.Resource{Kind: "host group", ID: g.ID, Name: g.Name})
	}
	plan.Changes = []safety.Change{
		{Field: "data collection", After: collectionLabel(req.CollectData)},
		{Field: "hosts affected", After: itoa(len(hosts))},
	}
	if !req.CollectData {
		plan.Changes = append(plan.Changes, safety.Change{
			Field: "note",
			After: "data collection stops; a real outage starting during this window will not be recorded",
		})
	}
	return plan, plan.Seal()
}

func collectionLabel(collect bool) string {
	if collect {
		return "continues"
	}
	return "stops"
}

type namedID struct {
	ID   string
	Name string
}

// expandHostPatterns turns names and wildcards into concrete hosts, failing
// loudly on a pattern that matched nothing rather than silently narrowing the
// window.
func (s *Service) expandHostPatterns(ctx context.Context, patterns []string) ([]namedID, error) {
	var out []namedID
	seen := map[string]bool{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		hosts, truncated, err := s.ListHosts(ctx, HostQuery{Search: p, Limit: 500})
		if err != nil {
			return nil, err
		}
		if truncated {
			return nil, errs.Usage("pattern %q matches more than 500 hosts; narrow it", p)
		}
		if len(hosts) == 0 {
			return nil, errs.NotFound("no host matches %q", p).
				WithSuggestion("patterns are substrings and may use *; check with 'zabbix-ai-cli-mcp host list --search %s'", p)
		}
		for _, h := range hosts {
			if !seen[h.ID] {
				seen[h.ID] = true
				out = append(out, namedID{ID: h.ID, Name: h.Name})
			}
		}
	}
	return out, nil
}

func (s *Service) expandGroupPatterns(ctx context.Context, patterns []string) ([]namedID, error) {
	var out []namedID
	seen := map[string]bool{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// The same exact-name rule as the read paths, and for a stronger
		// reason: this list decides which hosts stop alerting.
		groups, err := s.resolveHostGroups(ctx, p)
		if err != nil {
			return nil, err
		}
		if len(groups) == 0 {
			return nil, errs.NotFound("no host group matches %q", p)
		}
		for _, g := range groups {
			if !seen[g.ID] {
				seen[g.ID] = true
				out = append(out, g)
			}
		}
	}
	return out, nil
}

// PlanMaintenanceExtend moves a window's end further out.
func (s *Service) PlanMaintenanceExtend(ctx context.Context, profile, id string, by time.Duration) (*safety.Plan, error) {
	if by <= 0 {
		return nil, errs.Usage("give a positive duration to extend by")
	}
	m, err := s.GetMaintenance(ctx, id)
	if err != nil {
		return nil, err
	}
	current, err := time.Parse(time.RFC3339, m.ActiveTill)
	if err != nil {
		return nil, errs.Internal("maintenance %s has an unreadable end time", id)
	}
	newTill := current.Add(by).Truncate(time.Minute)
	since, err := time.Parse(time.RFC3339, m.ActiveSince)
	if err != nil {
		return nil, errs.Internal("maintenance %s has an unreadable start time", id)
	}
	period := int64(newTill.Sub(since).Seconds())
	if time.Duration(period)*time.Second > maxMaintenancePeriod {
		return nil, errs.Usage("extending by %s would exceed the maximum window length", HumanDuration(by))
	}

	plan, err := safety.NewPlan("maintenance.extend", profile, safety.RiskWrite, "maintenance")
	if err != nil {
		return nil, err
	}
	plan.Params = map[string]any{
		"maintenanceid": id,
		"active_till":   newTill.Unix(),
		"timeperiods": []map[string]any{{
			"timeperiod_type": 0,
			"start_date":      since.Unix(),
			"period":          period,
		}},
	}
	plan.ImpactCount = len(m.Hosts)
	plan.Summary = fmt.Sprintf("Extend maintenance %q by %s", m.Name, HumanDuration(by))
	plan.Changes = []safety.Change{{Field: "ends", Before: m.ActiveTill, After: rfc3339(newTill)}}
	plan.Resources = []safety.Resource{{Kind: "maintenance", ID: id, Name: m.Name}}
	plan.Preconditions = maintenanceStillExists(id, m.Name)
	return plan, plan.Seal()
}

// PlanMaintenanceExpire ends a window now without deleting its record.
func (s *Service) PlanMaintenanceExpire(ctx context.Context, profile, id string) (*safety.Plan, error) {
	m, err := s.GetMaintenance(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.Expired {
		return nil, errs.Usage("maintenance %q already ended at %s", m.Name, m.ActiveTill)
	}
	since, err := time.Parse(time.RFC3339, m.ActiveSince)
	if err != nil {
		return nil, errs.Internal("maintenance %s has an unreadable start time", id)
	}
	now := time.Now()
	if since.After(now) {
		// A window that has not begun cannot be ended. Moving its end to the
		// earliest legal moment would leave a shorter window still scheduled,
		// which looks like a cancellation and is not one.
		return nil, errs.Usage("maintenance %q has not started yet; it begins at %s", m.Name, m.ActiveSince).
			WithSuggestion("use 'zabbix-ai-cli-mcp maintenance delete %s' to cancel it", id)
	}
	// The window must keep a legal period, so it ends at the earliest moment
	// Zabbix will accept rather than exactly now.
	end := now.Truncate(time.Minute)
	if end.Sub(since) < minMaintenancePeriod {
		end = since.Add(minMaintenancePeriod)
	}
	plan, err := safety.NewPlan("maintenance.expire", profile, safety.RiskWrite, "maintenance")
	if err != nil {
		return nil, err
	}
	plan.Params = map[string]any{
		"maintenanceid": id,
		"active_till":   end.Unix(),
		"timeperiods": []map[string]any{{
			"timeperiod_type": 0,
			"start_date":      since.Unix(),
			"period":          int64(end.Sub(since).Seconds()),
		}},
	}
	plan.ImpactCount = len(m.Hosts)
	// Zabbix will not accept a window shorter than its minimum period, so a
	// window that started moments ago cannot end until that minimum is up.
	// The summary says when alerting actually resumes rather than "now",
	// which would be the same misleading report the guard above exists to
	// prevent.
	if end.After(now.Truncate(time.Minute)) {
		plan.Summary = fmt.Sprintf("End maintenance %q at %s, the earliest Zabbix allows for a window this new; %d host(s) resume alerting then",
			m.Name, rfc3339(end), len(m.Hosts))
	} else {
		plan.Summary = fmt.Sprintf("End maintenance %q now; %d host(s) resume alerting", m.Name, len(m.Hosts))
	}
	plan.Changes = []safety.Change{{Field: "ends", Before: m.ActiveTill, After: rfc3339(end)}}
	plan.Resources = []safety.Resource{{Kind: "maintenance", ID: id, Name: m.Name}}
	plan.Preconditions = maintenanceStillExists(id, m.Name)
	return plan, plan.Seal()
}

// PlanMaintenanceDelete removes a window entirely.
func (s *Service) PlanMaintenanceDelete(ctx context.Context, profile, id string) (*safety.Plan, error) {
	m, err := s.GetMaintenance(ctx, id)
	if err != nil {
		return nil, err
	}
	plan, err := safety.NewPlan("maintenance.delete", profile, safety.RiskDestructive, "maintenance")
	if err != nil {
		return nil, err
	}
	// maintenance.delete takes a flat array of identifiers, not an object.
	plan.Params = map[string]any{"maintenanceids": []string{id}}
	plan.ImpactCount = len(m.Hosts)
	plan.Summary = fmt.Sprintf("Delete maintenance %q permanently; %d host(s) resume alerting", m.Name, len(m.Hosts))
	plan.Resources = []safety.Resource{{Kind: "maintenance", ID: id, Name: m.Name}}
	plan.Changes = []safety.Change{{Field: "maintenance window", Before: m.Name}}
	plan.RequiresConfirmName = m.Name
	plan.Preconditions = maintenanceStillExists(id, m.Name)
	return plan, plan.Seal()
}

func maintenanceStillExists(id, name string) []safety.Precondition {
	one := 1
	return []safety.Precondition{{
		Description: fmt.Sprintf("maintenance %s is still named %q", id, name),
		Method:      "maintenance.get",
		Params: map[string]any{
			"output":         []string{"maintenanceid", "name"},
			"maintenanceids": []string{id},
		},
		ExpectCount: &one,
		ExpectField: map[string]any{"name": name},
	}}
}
