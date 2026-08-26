package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
)

// Alert delivery statuses as reported by alert.get.
const (
	alertStatusNotSent = "0"
	alertStatusSent    = "1"
	alertStatusFailed  = "2"
	alertStatusNew     = "3"
)

// EventSummary identifies the event an explanation is about.
type EventSummary struct {
	EventID      string        `json:"eventid"`
	Name         string        `json:"name"`
	Severity     string        `json:"severity"`
	SeverityCode int           `json:"severity_code"`
	Started      string        `json:"started"`
	Age          string        `json:"age,omitempty"`
	Resolved     bool          `json:"resolved"`
	Acknowledged bool          `json:"acknowledged"`
	Hosts        []ProblemHost `json:"hosts,omitempty"`
	TriggerID    string        `json:"triggerid,omitempty"`
}

// DeliveryAttempt is one notification Zabbix tried to send.
type DeliveryAttempt struct {
	AlertID   string `json:"alertid"`
	MediaType string `json:"media_type"`
	SendTo    string `json:"send_to,omitempty"`
	Status    string `json:"status"`
	Retries   int    `json:"retries"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Kind      string `json:"kind"`
}

// ActionCheck reports one trigger action and whether it could have fired.
type ActionCheck struct {
	ActionID   string   `json:"actionid"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	Operations int      `json:"operations"`
	Recipients []string `json:"recipients,omitempty"`
}

// MediaTypeCheck reports the state of a media type.
type MediaTypeCheck struct {
	ID      string `json:"mediatypeid"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// RecipientCheck reports whether one user could have received the event.
type RecipientCheck struct {
	User             string `json:"user"`
	MediaType        string `json:"media_type"`
	SendTo           string `json:"send_to,omitempty"`
	MediaEnabled     bool   `json:"media_enabled"`
	SeverityAccepted bool   `json:"severity_accepted"`
	Period           string `json:"period,omitempty"`
}

// AlertExplanation gathers every fact that bears on whether a notification was
// sent for an event.
//
// It states facts and leaves the conclusion to the reader: answering "why was
// there no alert" previously took three ad-hoc scripts, and the value is in
// collecting the chain reliably, not in guessing which link broke.
type AlertExplanation struct {
	Event        EventSummary      `json:"event"`
	Suppressed   bool              `json:"suppressed"`
	SuppressedBy []Suppression     `json:"suppressed_by,omitempty"`
	Attempts     []DeliveryAttempt `json:"delivery_attempts"`
	Actions      []ActionCheck     `json:"actions,omitempty"`
	MediaTypes   []MediaTypeCheck  `json:"media_types,omitempty"`
	Recipients   []RecipientCheck  `json:"recipients,omitempty"`
	Findings     []string          `json:"findings"`
	Partial      bool              `json:"-"`
	Warnings     []string          `json:"-"`
}

type wireEvent struct {
	EventID      string `json:"eventid"`
	Clock        string `json:"clock"`
	Name         string `json:"name"`
	Severity     string `json:"severity"`
	Acknowledged string `json:"acknowledged"`
	Value        string `json:"value"`
	ObjectID     string `json:"objectid"`
	RClock       string `json:"r_eventid"`
	Hosts        []struct {
		HostID string `json:"hostid"`
		Name   string `json:"name"`
	} `json:"hosts"`
	SuppressionData []struct {
		MaintenanceID string `json:"maintenanceid"`
		SuppressUntil string `json:"suppress_until"`
		UserID        string `json:"userid"`
	} `json:"suppression_data"`
}

// ExplainAlert assembles the notification chain for an event.
func (s *Service) ExplainAlert(ctx context.Context, eventID string) (*AlertExplanation, error) {
	ev, err := s.getEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	exp := &AlertExplanation{Event: ev.summary()}
	for _, sd := range ev.SuppressionData {
		sup := Suppression{MaintenanceID: sd.MaintenanceID, ByUser: sd.UserID != "" && sd.UserID != "0"}
		if until := unixToTime(sd.SuppressUntil); until.IsZero() {
			sup.Indefinite = true
		} else {
			sup.Until = rfc3339(until)
		}
		exp.SuppressedBy = append(exp.SuppressedBy, sup)
	}
	exp.Suppressed = len(exp.SuppressedBy) > 0
	if exp.Suppressed {
		if names, err := s.maintenanceNames(ctx, maintenanceIDs(exp.SuppressedBy)); err == nil {
			for i, sup := range exp.SuppressedBy {
				if n, ok := names[sup.MaintenanceID]; ok {
					exp.SuppressedBy[i].MaintenanceName = n
				}
			}
		}
		exp.Findings = append(exp.Findings, "the event is suppressed: "+SuppressionSummary(exp.SuppressedBy))
	}

	attempts, attemptsTruncated, err := s.deliveryAttempts(ctx, eventID)
	attemptsRead := err == nil
	if !attemptsRead {
		exp.Partial = true
		exp.Warnings = append(exp.Warnings, "delivery attempts could not be read: "+err.Error())
	}
	if attemptsTruncated {
		exp.Partial = true
		exp.Warnings = append(exp.Warnings, "delivery attempts were truncated at 100 newest records")
	}
	if attempts == nil {
		attempts = []DeliveryAttempt{}
	}
	exp.Attempts = attempts

	sent, failed := 0, 0
	for _, a := range attempts {
		switch a.Status {
		case "sent":
			sent++
		case "failed":
			failed++
		}
	}
	switch {
	case sent > 0 && failed == 0:
		exp.Findings = append(exp.Findings, fmt.Sprintf("Zabbix sent %d notification(s) for this event", sent))
	case failed > 0:
		exp.Findings = append(exp.Findings, fmt.Sprintf("%d of %d delivery attempts failed", failed, len(attempts)))
	case attemptsRead && len(attempts) == 0:
		exp.Findings = append(exp.Findings, "Zabbix recorded no delivery attempt for this event")
	case attemptsRead && len(attempts) > 0:
		exp.Findings = append(exp.Findings, fmt.Sprintf("Zabbix recorded %d queued or not-sent notification attempt(s)", len(attempts)))
	}

	// The configuration chain only needs inspecting when nothing was sent.
	if attemptsRead && len(attempts) == 0 {
		if err := s.inspectNotificationConfig(ctx, exp); err != nil {
			exp.Partial = true
			exp.Warnings = append(exp.Warnings, err.Error())
		} else {
			exp.Partial = true
			exp.Warnings = append(exp.Warnings,
				"configuration fallback lists candidates only; action conditions, escalation steps, and media active periods are not evaluated at the event time")
		}
	}
	if len(exp.Findings) == 0 {
		if exp.Partial {
			exp.Findings = append(exp.Findings, "notification diagnosis is incomplete")
		} else {
			exp.Findings = append(exp.Findings, "no obstacle to notification was found in the configuration")
		}
	}
	return exp, nil
}

func maintenanceIDs(sups []Suppression) []string {
	ids := make([]string, 0, len(sups))
	for _, s := range sups {
		if s.MaintenanceID != "" && s.MaintenanceID != "0" {
			ids = append(ids, s.MaintenanceID)
		}
	}
	return ids
}

func (s *Service) getEvent(ctx context.Context, eventID string) (*wireEvent, error) {
	params := map[string]any{
		"output":                []string{"eventid", "clock", "name", "severity", "acknowledged", "value", "objectid", "r_eventid"},
		"eventids":              []string{eventID},
		"selectHosts":           []string{"hostid", "name"},
		"selectSuppressionData": "extend",
	}
	var events []wireEvent
	if err := s.client.CallIdempotent(ctx, "event.get", params, &events); err != nil {
		return nil, errs.FromAPI(err)
	}
	if len(events) == 0 {
		return nil, errs.NotFound("event %s does not exist", eventID).
			WithSuggestion("event IDs come from 'zabbix-ai-cli-mcp problems list'; a pasted alert can be resolved with 'zabbix-ai-cli-mcp resolve'")
	}
	return &events[0], nil
}

func (e *wireEvent) summary() EventSummary {
	started := unixToTime(e.Clock)
	sum := EventSummary{
		EventID:      e.EventID,
		Name:         output.Sanitise(e.Name),
		Severity:     SeverityName(e.Severity),
		SeverityCode: atoi(e.Severity),
		Started:      rfc3339(started),
		Acknowledged: e.Acknowledged == "1",
		Resolved:     e.RClock != "" && e.RClock != "0",
		TriggerID:    e.ObjectID,
	}
	if !started.IsZero() {
		sum.Age = HumanDuration(time.Since(started))
	}
	for _, h := range e.Hosts {
		sum.Hosts = append(sum.Hosts, ProblemHost{ID: h.HostID, Name: output.Sanitise(h.Name)})
	}
	return sum
}

func (s *Service) deliveryAttempts(ctx context.Context, eventID string) ([]DeliveryAttempt, bool, error) {
	var wire []struct {
		AlertID     string `json:"alertid"`
		MediaTypeID string `json:"mediatypeid"`
		SendTo      string `json:"sendto"`
		Status      string `json:"status"`
		Retries     string `json:"retries"`
		Error       string `json:"error"`
		Clock       string `json:"clock"`
		AlertType   string `json:"alerttype"`
	}
	params := map[string]any{
		"output":    []string{"alertid", "mediatypeid", "sendto", "status", "retries", "error", "clock", "alerttype"},
		"eventids":  []string{eventID},
		"sortfield": "clock",
		"sortorder": "DESC",
		"limit":     101,
	}
	if err := s.client.CallIdempotent(ctx, "alert.get", params, &wire); err != nil {
		return nil, false, errs.FromAPI(err)
	}
	wire, truncated := output.Bound(wire, 100)
	names, _ := s.mediaTypeNames(ctx)
	attempts := make([]DeliveryAttempt, 0, len(wire))
	for _, w := range wire {
		a := DeliveryAttempt{
			AlertID:   w.AlertID,
			MediaType: names[w.MediaTypeID],
			SendTo:    output.Sanitise(w.SendTo),
			Status:    alertStatusName(w.Status),
			Retries:   atoi(w.Retries),
			Error:     output.Sanitise(w.Error),
			Timestamp: rfc3339(unixToTime(w.Clock)),
			Kind:      alertKind(w.AlertType),
		}
		if a.MediaType == "" {
			a.MediaType = "media type " + w.MediaTypeID
		}
		attempts = append(attempts, a)
	}
	return attempts, truncated, nil
}

func alertStatusName(v string) string {
	switch v {
	case alertStatusNotSent:
		return "not sent"
	case alertStatusSent:
		return "sent"
	case alertStatusFailed:
		return "failed"
	case alertStatusNew:
		return "queued"
	default:
		return "unknown"
	}
}

func alertKind(v string) string {
	if v == "1" {
		return "remote command"
	}
	return "message"
}

func (s *Service) mediaTypeNames(ctx context.Context) (map[string]string, error) {
	types, err := s.mediaTypes(ctx)
	if err != nil {
		return map[string]string{}, err
	}
	names := make(map[string]string, len(types))
	for _, t := range types {
		names[t.ID] = t.Name
	}
	return names, nil
}

func (s *Service) mediaTypes(ctx context.Context) ([]MediaTypeCheck, error) {
	var wire []struct {
		MediaTypeID string `json:"mediatypeid"`
		Name        string `json:"name"`
		Status      string `json:"status"`
	}
	params := map[string]any{"output": []string{"mediatypeid", "name", "status"}}
	if err := s.client.CallIdempotent(ctx, "mediatype.get", params, &wire); err != nil {
		return nil, errs.FromAPI(err)
	}
	out := make([]MediaTypeCheck, 0, len(wire))
	for _, w := range wire {
		out = append(out, MediaTypeCheck{
			ID: w.MediaTypeID, Name: output.Sanitise(w.Name), Enabled: w.Status == "0",
		})
	}
	return out, nil
}

type wireAction struct {
	ActionID    string `json:"actionid"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	EventSource string `json:"eventsource"`
	Operations  []struct {
		OperationType string `json:"operationtype"`
		OpMessage     struct {
			MediaTypeID string `json:"mediatypeid"`
		} `json:"opmessage"`
		OpMessageGrp []struct {
			UsrGrpID string `json:"usrgrpid"`
		} `json:"opmessage_grp"`
		OpMessageUsr []struct {
			UserID string `json:"userid"`
		} `json:"opmessage_usr"`
	} `json:"operations"`
}

type notificationCandidate struct {
	MediaTypeID string
	GroupIDs    []string
	UserIDs     []string
}

// inspectNotificationConfig collects candidate notification configuration.
// Matching action conditions and time-dependent rules requires server-side
// evaluation that is not exposed by these API responses.
func (s *Service) inspectNotificationConfig(ctx context.Context, exp *AlertExplanation) error {
	var actions []wireAction
	params := map[string]any{
		"output":           []string{"actionid", "name", "status", "eventsource"},
		"filter":           map[string]any{"eventsource": 0},
		"selectOperations": "extend",
		"limit":            51,
	}
	if err := s.client.CallIdempotent(ctx, "action.get", params, &actions); err != nil {
		return errs.FromAPI(err)
	}
	actions, actionsTruncated := output.Bound(actions, 50)
	if actionsTruncated {
		exp.Partial = true
		exp.Warnings = append(exp.Warnings, "trigger actions were truncated at 50 records")
	}

	enabled := 0
	var candidates []notificationCandidate
	for _, a := range actions {
		check := ActionCheck{
			ActionID:   a.ActionID,
			Name:       output.Sanitise(a.Name),
			Enabled:    a.Status == "0",
			Operations: len(a.Operations),
		}
		if check.Enabled {
			enabled++
			for _, op := range a.Operations {
				if op.OperationType != "0" {
					continue
				}
				candidate := notificationCandidate{MediaTypeID: op.OpMessage.MediaTypeID}
				for _, g := range op.OpMessageGrp {
					candidate.GroupIDs = appendUnique(candidate.GroupIDs, g.UsrGrpID)
				}
				for _, u := range op.OpMessageUsr {
					candidate.UserIDs = appendUnique(candidate.UserIDs, u.UserID)
				}
				if len(candidate.GroupIDs) > 0 || len(candidate.UserIDs) > 0 {
					candidates = append(candidates, candidate)
				}
			}
		}
		exp.Actions = append(exp.Actions, check)
	}
	switch {
	case len(actions) == 0:
		exp.Findings = append(exp.Findings, "no trigger action exists, so no notification can ever be sent")
	case enabled == 0 && !actionsTruncated:
		exp.Findings = append(exp.Findings, "every trigger action is disabled")
	}

	types, err := s.mediaTypes(ctx)
	if err != nil {
		return err
	}
	exp.MediaTypes = types
	enabledTypes := 0
	var disabled []string
	for _, t := range types {
		if t.Enabled {
			enabledTypes++
		} else {
			disabled = append(disabled, t.Name)
		}
	}
	if enabledTypes == 0 && len(types) > 0 {
		exp.Findings = append(exp.Findings, "every media type is disabled")
	} else if len(disabled) > 0 {
		exp.Findings = append(exp.Findings, "disabled media types: "+strings.Join(disabled, ", "))
	}

	if enabled > 0 {
		if err := s.checkRecipients(ctx, exp, candidates, types); err != nil {
			return err
		}
	}
	return nil
}

type wireRecipientUser struct {
	UserID   string `json:"userid"`
	Username string `json:"username"`
	Medias   []struct {
		MediaTypeID string `json:"mediatypeid"`
		SendTo      any    `json:"sendto"`
		Active      string `json:"active"`
		Severity    string `json:"severity"`
		Period      string `json:"period"`
	} `json:"medias"`
}

// checkRecipients checks static media enablement and severity filters for each
// candidate recipient. Active periods are returned for diagnosis but are not
// interpreted here.
//
// A user media carries a severity bitmask and an active period; either can
// drop a notification without any error being recorded anywhere.
func (s *Service) checkRecipients(ctx context.Context, exp *AlertExplanation, candidates []notificationCandidate, types []MediaTypeCheck) error {
	if len(candidates) == 0 {
		exp.Findings = append(exp.Findings, "no enabled action names any user or user group to notify")
		return nil
	}
	typeByID := make(map[string]MediaTypeCheck, len(types))
	for _, mediaType := range types {
		typeByID[mediaType.ID] = mediaType
	}
	usersTruncated := false
	// Actions overwhelmingly reuse the same operator group, and every message
	// operation of every action contributes a candidate. Without this the
	// walk is two serial user.get calls per candidate, which on a site with a
	// few dozen notifying actions runs the explanation past the client
	// timeout — the command answers nothing instead of answering slowly.
	cached := make(map[string][]wireRecipientUser)
	fetch := func(filter string, ids []string) ([]wireRecipientUser, error) {
		if len(ids) == 0 {
			return nil, nil
		}
		key := cacheKey(filter, ids)
		if hit, ok := cached[key]; ok {
			return hit, nil
		}
		params := map[string]any{
			"output":       []string{"userid", "username", "name"},
			"selectMedias": "extend",
			"limit":        101,
			filter:         ids,
		}
		var batch []wireRecipientUser
		if err := s.client.CallIdempotent(ctx, "user.get", params, &batch); err != nil {
			return nil, errs.FromAPI(err)
		}
		var truncated bool
		batch, truncated = output.Bound(batch, 100)
		if truncated {
			usersTruncated = true
		}
		cached[key] = batch
		return batch, nil
	}
	severityBit := 1 << exp.Event.SeverityCode
	reachable := 0
	withMedia := 0
	emitted := make(map[string]struct{})
	for _, candidate := range candidates {
		// Zabbix treats multiple user.get filters as an intersection. Resolve
		// action groups and explicitly named users separately, then union them
		// within this operation so its selected media type is preserved.
		groupUsers, err := fetch("usrgrpids", candidate.GroupIDs)
		if err != nil {
			return err
		}
		directUsers, err := fetch("userids", candidate.UserIDs)
		if err != nil {
			return err
		}
		users := append(groupUsers, directUsers...)
		seenUsers := make(map[string]struct{}, len(users))
		for _, u := range users {
			if _, seen := seenUsers[u.UserID]; seen {
				continue
			}
			seenUsers[u.UserID] = struct{}{}
			for _, m := range u.Medias {
				if candidate.MediaTypeID != "" && candidate.MediaTypeID != "0" && candidate.MediaTypeID != m.MediaTypeID {
					continue
				}
				key := u.UserID + "\x00" + m.MediaTypeID + "\x00" + sendToString(m.SendTo)
				if _, seen := emitted[key]; seen {
					continue
				}
				emitted[key] = struct{}{}
				withMedia++
				accepted := atoi(m.Severity)&severityBit != 0
				userMediaEnabled := m.Active == "0"
				mediaType, knownMediaType := typeByID[m.MediaTypeID]
				mediaTypeEnabled := knownMediaType && mediaType.Enabled
				name := mediaType.Name
				if name == "" {
					name = "media type " + m.MediaTypeID
				}
				exp.Recipients = append(exp.Recipients, RecipientCheck{
					User:             output.Sanitise(u.Username),
					MediaType:        name,
					SendTo:           output.Sanitise(sendToString(m.SendTo)),
					MediaEnabled:     userMediaEnabled && mediaTypeEnabled,
					SeverityAccepted: accepted,
					Period:           output.Sanitise(m.Period),
				})
				if accepted && userMediaEnabled && mediaTypeEnabled {
					reachable++
				}
			}
		}
	}
	if usersTruncated {
		exp.Partial = true
		exp.Warnings = append(exp.Warnings, "candidate recipients were truncated at 100 users for at least one action operation")
	}
	switch {
	case withMedia == 0 && !usersTruncated:
		exp.Findings = append(exp.Findings, "no notified user has media selected by an enabled action")
	case reachable == 0 && !usersTruncated:
		exp.Findings = append(exp.Findings,
			fmt.Sprintf("no action-selected user media accepts severity %q; every entry, media type, or severity filter blocks delivery", exp.Event.Severity))
	}
	return nil
}

// sendToString normalises the sendto field, which Zabbix returns as a string
// for most media types and as an array for email.
func sendToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return ""
	}
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// cacheKey identifies one user.get lookup by its filter and the set of IDs it
// asks for, so the same set asked for twice costs one round trip.
func cacheKey(filter string, ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return filter + "\x00" + strings.Join(sorted, ",")
}
