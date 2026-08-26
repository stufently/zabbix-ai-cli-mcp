package service

import (
	"context"
	"errors"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
)

// HostStatus is the high-level operational state of one host.
type HostStatus struct {
	Host             string      `json:"host"`
	HostID           string      `json:"hostid"`
	Monitored        bool        `json:"monitored"`
	AgentAvailable   string      `json:"agent_available"`
	InMaintenance    bool        `json:"in_maintenance"`
	MaintenanceName  string      `json:"maintenance_name,omitempty"`
	ActiveProblems   int         `json:"active_problems"`
	SuppressedCount  int         `json:"suppressed_problems"`
	HighestSeverity  string      `json:"highest_severity,omitempty"`
	LastDataAgeSecs  *int64      `json:"last_data_age_seconds,omitempty"`
	StaleItems       int         `json:"stale_items"`
	UnsupportedItems int         `json:"unsupported_items"`
	Interfaces       []Interface `json:"interfaces,omitempty"`
}

// Investigation is the diagnostic snapshot behind `host investigate`.
//
// It folds what used to be four or five separate API round trips into one
// call. The command collects facts and stops there; interpreting them is the
// reader's job, not this program's.
type Investigation struct {
	Host        Host          `json:"host"`
	Status      HostStatus    `json:"status"`
	Problems    []Problem     `json:"active_problems"`
	Recent      []EventRecord `json:"recent_events,omitempty"`
	NoData      []Value       `json:"no_data_items,omitempty"`
	Unsupported []Item        `json:"unsupported_items,omitempty"`
	Maintenance []Maintenance `json:"maintenance,omitempty"`
	Warnings    []string      `json:"-"`
	Partial     bool          `json:"-"`
}

// EventRecord is a past event on the host.
type EventRecord struct {
	EventID  string `json:"eventid"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Started  string `json:"started"`
	Resolved bool   `json:"resolved"`
}

// StatusOf returns the compact operational state of a host.
func (s *Service) StatusOf(ctx context.Context, pattern string) (*HostStatus, error) {
	inv, err := s.investigate(ctx, pattern, false)
	if err != nil {
		return nil, err
	}
	return &inv.Status, nil
}

// Investigate collects a diagnostic snapshot of one host.
func (s *Service) Investigate(ctx context.Context, pattern string) (*Investigation, error) {
	return s.investigate(ctx, pattern, true)
}

// recentEventWindow bounds the history an investigation reads.
const recentEventWindow = 24 * time.Hour

func (s *Service) investigate(ctx context.Context, pattern string, full bool) (*Investigation, error) {
	host, err := s.ResolveHost(ctx, pattern)
	if err != nil {
		return nil, err
	}
	inv := &Investigation{Host: host}
	inv.Status = HostStatus{
		Host:           host.Name,
		HostID:         host.ID,
		Monitored:      host.Monitored,
		AgentAvailable: host.AgentAvailable,
		InMaintenance:  host.InMaintenance,
		Interfaces:     host.Interfaces,
	}

	var problems []Problem
	var events []EventRecord
	var items []wireItem
	var windows []Maintenance

	tasks := []task{
		{name: "active problems", run: func(ctx context.Context) error {
			p, _, err := s.ListProblems(ctx, ProblemQuery{Host: host.ID, Limit: 100})
			problems = p
			return err
		}},
		{name: "items", run: func(ctx context.Context) error {
			it, _, err := s.resolveItems(ctx, host.ID, ItemQuery{Limit: 500, EnabledOnly: true})
			items = it
			return err
		}},
	}
	if full {
		tasks = append(tasks,
			task{name: "recent events", run: func(ctx context.Context) error {
				e, err := s.recentEvents(ctx, host.ID, recentEventWindow, 20)
				events = e
				return err
			}},
			task{name: "maintenance", run: func(ctx context.Context) error {
				m, _, err := s.ListMaintenance(ctx, MaintenanceQuery{HostID: host.ID, Limit: 20})
				windows = m
				return err
			}},
		)
	}

	for _, f := range runParallel(ctx, tasks) {
		inv.Partial = true
		inv.Warnings = append(inv.Warnings, f)
	}

	inv.Problems = problems
	inv.Recent = events
	inv.Maintenance = windows
	inv.Status.ActiveProblems = len(problems)
	highest := -1
	for _, p := range problems {
		if p.Suppressed {
			inv.Status.SuppressedCount++
		}
		if p.SeverityCode > highest {
			highest = p.SeverityCode
			inv.Status.HighestSeverity = p.Severity
		}
	}
	for _, w := range windows {
		if w.Active {
			inv.Status.MaintenanceName = w.Name
			break
		}
	}

	if len(items) > 0 {
		s.assessItems(ctx, inv, items)
	}
	return inv, nil
}

// assessItems separates unsupported items from silent ones and computes the
// freshest data timestamp on the host.
//
// Staleness is not decided by item.lastclock: that field has returned a
// constant zero for several releases. Nor is it decided for items that do not
// collect on a schedule, because a trapper item that has sent nothing is not
// evidence of a fault.
func (s *Service) assessItems(ctx context.Context, inv *Investigation, items []wireItem) {
	var scheduled []wireItem
	for _, w := range items {
		if w.State == "1" {
			inv.Status.UnsupportedItems++
			if len(inv.Unsupported) < 20 {
				inv.Unsupported = append(inv.Unsupported, w.toItem())
			}
			continue
		}
		if w.collectsOnSchedule() && w.ValueType != valueTypeBinary {
			scheduled = append(scheduled, w)
		}
	}
	// Reading every item's newest value would mean one query per item. Cap the
	// sample so an investigation stays a single quick call rather than a load
	// test, and say so when the cap bites.
	const sampleSize = 40
	sample := scheduled
	if len(sample) > sampleSize {
		// Items arrive sorted by name, so taking the first forty would only
		// ever look at the start of the alphabet. Positions are computed as a
		// proportion of the list rather than by a whole-number stride: a
		// stride rounds down to 1 for anything under eighty items and quietly
		// degenerates back into "the first forty", and it never reaches the
		// tail of a longer list at all.
		sample = make([]wireItem, 0, sampleSize)
		for k := 0; k < sampleSize; k++ {
			sample = append(sample, scheduled[k*len(scheduled)/sampleSize])
		}
		inv.Warnings = append(inv.Warnings,
			"no-data detection sampled "+itoa(len(sample))+" of "+itoa(len(scheduled))+
				" scheduled items, spread across the list")
	}
	if len(sample) == 0 {
		return
	}
	values, err := s.latestForItems(ctx, sample)
	var partial errPartialHistory
	switch {
	case errors.As(err, &partial):
		// A read that failed is not an item without data, and saying so would
		// report healthy monitoring as broken.
		inv.Partial = true
		inv.Warnings = append(inv.Warnings,
			"some item freshness could not be read, so no-data counts understate: "+partial.Error())
	case err != nil:
		inv.Partial = true
		inv.Warnings = append(inv.Warnings, "item freshness could not be read: "+err.Error())
		return
	}
	var newest int64 = -1
	for _, v := range values {
		if v.NoData || v.Stale {
			inv.Status.StaleItems++
			if len(inv.NoData) < 20 {
				inv.NoData = append(inv.NoData, v)
			}
		}
		if !v.NoData && (newest < 0 || v.AgeSeconds < newest) {
			newest = v.AgeSeconds
		}
	}
	if newest >= 0 {
		inv.Status.LastDataAgeSecs = &newest
	}
}

func (s *Service) recentEvents(ctx context.Context, hostID string, window time.Duration, limit int) ([]EventRecord, error) {
	params := map[string]any{
		"output":    []string{"eventid", "clock", "name", "severity", "r_eventid", "value"},
		"hostids":   []string{hostID},
		"source":    0,
		"object":    0,
		"time_from": time.Now().Add(-window).Unix(),
		"sortfield": []string{"eventid"},
		"sortorder": "DESC",
		"limit":     limit,
	}
	var wire []struct {
		EventID  string `json:"eventid"`
		Clock    string `json:"clock"`
		Name     string `json:"name"`
		Severity string `json:"severity"`
		REventID string `json:"r_eventid"`
		Value    string `json:"value"`
	}
	if err := s.client.CallIdempotent(ctx, "event.get", params, &wire); err != nil {
		return nil, err
	}
	out := make([]EventRecord, 0, len(wire))
	for _, w := range wire {
		out = append(out, EventRecord{
			EventID:  w.EventID,
			Name:     output.Sanitise(w.Name),
			Severity: SeverityName(w.Severity),
			Started:  rfc3339(unixToTime(w.Clock)),
			Resolved: w.REventID != "" && w.REventID != "0",
		})
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
