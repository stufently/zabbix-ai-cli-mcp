package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
)

type wireProblem struct {
	EventID      string    `json:"eventid"`
	ObjectID     string    `json:"objectid"`
	Clock        string    `json:"clock"`
	Name         string    `json:"name"`
	Severity     string    `json:"severity"`
	Acknowledged string    `json:"acknowledged"`
	Suppressed   string    `json:"suppressed"`
	CauseEventID string    `json:"cause_eventid"`
	Opdata       string    `json:"opdata"`
	RClock       string    `json:"r_clock"`
	Tags         []wireTag `json:"tags"`

	SuppressionData []struct {
		MaintenanceID string `json:"maintenanceid"`
		SuppressUntil string `json:"suppress_until"`
		UserID        string `json:"userid"`
	} `json:"suppression_data"`
}

// Suppression explains why a problem is hidden.
//
// Naming the window matters: an outage on this installation stayed invisible
// for a month inside a maintenance window nobody remembered creating.
type Suppression struct {
	MaintenanceID   string `json:"maintenance_id,omitempty"`
	MaintenanceName string `json:"maintenance_name,omitempty"`
	Until           string `json:"until,omitempty"`
	Indefinite      bool   `json:"indefinite"`
	ByUser          bool   `json:"by_user"`
}

// ProblemHost identifies the host a problem belongs to.
type ProblemHost struct {
	ID   string `json:"hostid"`
	Name string `json:"name"`
}

// Problem is the compact projection of an active Zabbix problem.
type Problem struct {
	EventID      string        `json:"eventid"`
	Name         string        `json:"name"`
	Severity     string        `json:"severity"`
	SeverityCode int           `json:"severity_code"`
	Started      string        `json:"started"`
	AgeSeconds   int64         `json:"age_seconds"`
	Age          string        `json:"age"`
	Acknowledged bool          `json:"acknowledged"`
	Suppressed   bool          `json:"suppressed"`
	SuppressedBy []Suppression `json:"suppressed_by,omitempty"`
	Hosts        []ProblemHost `json:"hosts,omitempty"`
	TriggerID    string        `json:"triggerid,omitempty"`
	CauseEventID string        `json:"cause_eventid,omitempty"`
	Opdata       string        `json:"opdata,omitempty"`
	Tags         []Tag         `json:"tags,omitempty"`
}

var problemOutputFields = []string{
	"eventid", "objectid", "clock", "name", "severity",
	"acknowledged", "suppressed", "cause_eventid", "opdata",
}

// ProblemQuery selects active problems.
type ProblemQuery struct {
	Host        string
	Group       string
	MinSeverity int
	Since       time.Duration
	Limit       int
	// ExcludeSuppressed hides problems inside a maintenance window. The
	// default is to show them, with the suppressing window named.
	ExcludeSuppressed bool
	// UnacknowledgedOnly restricts the result to problems nobody has taken.
	UnacknowledgedOnly bool
	Tags               []Tag
}

// ListProblems returns active problems, newest first.
func (s *Service) ListProblems(ctx context.Context, q ProblemQuery) ([]Problem, bool, error) {
	params := map[string]any{
		"output":                problemOutputFields,
		"selectTags":            "extend",
		"selectSuppressionData": "extend",
		"sortfield":             []string{"eventid"},
		"sortorder":             "DESC",
		"recent":                false,
	}
	if q.Limit > 0 {
		params["limit"] = q.Limit + 1
	}
	if q.MinSeverity > 0 {
		severities := make([]int, 0, 6)
		for i := q.MinSeverity; i <= 5; i++ {
			severities = append(severities, i)
		}
		params["severities"] = severities
	}
	if q.Since > 0 {
		params["time_from"] = time.Now().Add(-q.Since).Unix()
	}
	if q.ExcludeSuppressed {
		params["suppressed"] = false
	}
	if q.UnacknowledgedOnly {
		params["acknowledged"] = false
	}
	if len(q.Tags) > 0 {
		tags := make([]map[string]any, 0, len(q.Tags))
		for _, t := range q.Tags {
			tag := map[string]any{"tag": t.Tag, "operator": 0}
			if t.Value != "" {
				tag["value"] = t.Value
			} else {
				tag["operator"] = 2 // "Contains", which with an empty value means "tag exists"
			}
			tags = append(tags, tag)
		}
		params["tags"] = tags
	}
	if q.Host != "" {
		h, err := s.ResolveHost(ctx, q.Host)
		if err != nil {
			return nil, false, err
		}
		params["hostids"] = []string{h.ID}
	} else if q.Group != "" {
		gids, err := s.hostGroupIDs(ctx, q.Group)
		if err != nil {
			return nil, false, err
		}
		if len(gids) == 0 {
			return nil, false, errs.NotFound("no host group matches %q", q.Group)
		}
		params["groupids"] = gids
	}

	var wire []wireProblem
	if err := s.client.CallIdempotent(ctx, "problem.get", params, &wire); err != nil {
		return nil, false, errs.FromAPI(err)
	}
	kept, truncated := output.Bound(wire, q.Limit)

	problems := make([]Problem, 0, len(kept))
	for _, w := range kept {
		problems = append(problems, w.toProblem())
	}
	if err := s.attachProblemHosts(ctx, problems); err != nil {
		return nil, false, err
	}
	if err := s.attachSuppressionNames(ctx, problems); err != nil {
		return nil, false, err
	}
	return problems, truncated, nil
}

func (w wireProblem) toProblem() Problem {
	started := unixToTime(w.Clock)
	p := Problem{
		EventID:      w.EventID,
		Name:         output.Sanitise(w.Name),
		Severity:     SeverityName(w.Severity),
		SeverityCode: atoi(w.Severity),
		Started:      rfc3339(started),
		Acknowledged: w.Acknowledged == "1",
		Suppressed:   w.Suppressed == "1",
		TriggerID:    w.ObjectID,
		Opdata:       output.Sanitise(w.Opdata),
	}
	if !started.IsZero() {
		age := time.Since(started)
		p.AgeSeconds = int64(age.Seconds())
		p.Age = HumanDuration(age)
	}
	if w.CauseEventID != "" && w.CauseEventID != "0" {
		p.CauseEventID = w.CauseEventID
	}
	for _, t := range w.Tags {
		p.Tags = append(p.Tags, Tag{Tag: output.Sanitise(t.Tag), Value: output.Sanitise(t.Value)})
	}
	for _, sd := range w.SuppressionData {
		sup := Suppression{MaintenanceID: sd.MaintenanceID, ByUser: sd.UserID != "" && sd.UserID != "0"}
		until := unixToTime(sd.SuppressUntil)
		if until.IsZero() {
			sup.Indefinite = true
		} else {
			sup.Until = rfc3339(until)
		}
		p.SuppressedBy = append(p.SuppressedBy, sup)
	}
	return p
}

// attachProblemHosts resolves each problem's host.
//
// problem.get has no selectHosts parameter, so the hosts arrive through a
// single batched trigger.get keyed on the problem's objectid.
func (s *Service) attachProblemHosts(ctx context.Context, problems []Problem) error {
	ids := make([]string, 0, len(problems))
	seen := map[string]bool{}
	for _, p := range problems {
		if p.TriggerID != "" && !seen[p.TriggerID] {
			seen[p.TriggerID] = true
			ids = append(ids, p.TriggerID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var triggers []struct {
		TriggerID string `json:"triggerid"`
		Hosts     []struct {
			HostID string `json:"hostid"`
			Name   string `json:"name"`
		} `json:"hosts"`
	}
	params := map[string]any{
		"output":      []string{"triggerid"},
		"triggerids":  ids,
		"selectHosts": []string{"hostid", "name"},
	}
	if err := s.client.CallIdempotent(ctx, "trigger.get", params, &triggers); err != nil {
		return errs.FromAPI(err)
	}
	byTrigger := make(map[string][]ProblemHost, len(triggers))
	for _, t := range triggers {
		for _, h := range t.Hosts {
			byTrigger[t.TriggerID] = append(byTrigger[t.TriggerID], ProblemHost{
				ID: h.HostID, Name: output.Sanitise(h.Name),
			})
		}
	}
	for i := range problems {
		problems[i].Hosts = byTrigger[problems[i].TriggerID]
	}
	return nil
}

// attachSuppressionNames turns maintenance IDs into names, so that a
// suppressed problem says which window hides it.
func (s *Service) attachSuppressionNames(ctx context.Context, problems []Problem) error {
	ids := map[string]bool{}
	for _, p := range problems {
		for _, sup := range p.SuppressedBy {
			if sup.MaintenanceID != "" && sup.MaintenanceID != "0" {
				ids[sup.MaintenanceID] = true
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	names, err := s.maintenanceNames(ctx, output.SortedKeys(ids))
	if err != nil {
		// A missing name is cosmetic; the ID still identifies the window.
		return nil
	}
	for i := range problems {
		for j, sup := range problems[i].SuppressedBy {
			if n, ok := names[sup.MaintenanceID]; ok {
				problems[i].SuppressedBy[j].MaintenanceName = n
			}
		}
	}
	return nil
}

func (s *Service) maintenanceNames(ctx context.Context, ids []string) (map[string]string, error) {
	var wire []struct {
		MaintenanceID string `json:"maintenanceid"`
		Name          string `json:"name"`
	}
	params := map[string]any{
		"output":         []string{"maintenanceid", "name"},
		"maintenanceids": ids,
	}
	if err := s.client.CallIdempotent(ctx, "maintenance.get", params, &wire); err != nil {
		return nil, errs.FromAPI(err)
	}
	names := make(map[string]string, len(wire))
	for _, w := range wire {
		names[w.MaintenanceID] = output.Sanitise(w.Name)
	}
	return names, nil
}

// GetProblem returns a single problem by event ID.
func (s *Service) GetProblem(ctx context.Context, eventID string) (*Problem, error) {
	params := map[string]any{
		"output":                problemOutputFields,
		"eventids":              []string{eventID},
		"selectTags":            "extend",
		"selectSuppressionData": "extend",
		"recent":                true,
	}
	var wire []wireProblem
	if err := s.client.CallIdempotent(ctx, "problem.get", params, &wire); err != nil {
		return nil, errs.FromAPI(err)
	}
	if len(wire) == 0 {
		return nil, errs.NotFound("no active problem has event ID %s", eventID).
			WithSuggestion("the problem may already be resolved; 'zabbix-ai-cli-mcp alert why %s' still works for resolved events", eventID)
	}
	p := wire[0].toProblem()
	list := []Problem{p}
	if err := s.attachProblemHosts(ctx, list); err != nil {
		return nil, err
	}
	if err := s.attachSuppressionNames(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// SuppressionSummary renders the suppression reason for a table cell.
func SuppressionSummary(sups []Suppression) string {
	if len(sups) == 0 {
		return ""
	}
	parts := make([]string, 0, len(sups))
	for _, s := range sups {
		name := s.MaintenanceName
		if name == "" && s.MaintenanceID != "" {
			name = "maintenance " + s.MaintenanceID
		}
		if s.ByUser {
			name = "suppressed by user"
		}
		switch {
		case s.Indefinite:
			parts = append(parts, fmt.Sprintf("%s (indefinite)", name))
		case s.Until != "":
			parts = append(parts, fmt.Sprintf("%s until %s", name, s.Until))
		default:
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, "; ")
}
