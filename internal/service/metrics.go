package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
)

// Zabbix value types, which double as history table selectors.
const (
	valueTypeFloat  = "0"
	valueTypeChar   = "1"
	valueTypeLog    = "2"
	valueTypeUint   = "3"
	valueTypeText   = "4"
	valueTypeBinary = "5"
)

type wireItem struct {
	ItemID    string `json:"itemid"`
	HostID    string `json:"hostid"`
	Name      string `json:"name"`
	Key       string `json:"key_"`
	ValueType string `json:"value_type"`
	Units     string `json:"units"`
	Delay     string `json:"delay"`
	Status    string `json:"status"`
	State     string `json:"state"`
	Error     string `json:"error"`
	Type      string `json:"type"`
}

var itemOutputFields = []string{
	"itemid", "hostid", "name", "key_", "value_type", "units", "delay", "status", "state", "error", "type",
}

// Item is the compact projection of a Zabbix item.
type Item struct {
	ID        string `json:"itemid"`
	HostID    string `json:"hostid"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	ValueType string `json:"value_type"`
	Units     string `json:"units,omitempty"`
	Delay     string `json:"delay,omitempty"`
	Enabled   bool   `json:"enabled"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

func (w wireItem) toItem() Item {
	return Item{
		ID:        w.ItemID,
		HostID:    w.HostID,
		Name:      output.Sanitise(w.Name),
		Key:       output.Sanitise(w.Key),
		ValueType: w.ValueType,
		Units:     output.Sanitise(w.Units),
		Delay:     w.Delay,
		Enabled:   w.Status == "0",
		Supported: w.State == "0",
		Error:     output.Sanitise(w.Error),
	}
}

// collectsOnSchedule reports whether an item is expected to produce data at a
// fixed interval. Trapper and dependent items arrive when something else sends
// or computes them, so silence from one is not evidence of a fault.
func (w wireItem) collectsOnSchedule() bool {
	switch w.Type {
	case "2", "18": // Zabbix trapper, dependent item
		return false
	}
	switch strings.TrimSpace(w.Delay) {
	case "", "0", "0s":
		return false
	}
	return true
}

// Value is one observation of an item.
type Value struct {
	Item
	Value      string `json:"value"`
	Raw        string `json:"raw_value,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
	Age        string `json:"age,omitempty"`
	// NoData marks an item that produced nothing inside the search window.
	NoData bool `json:"no_data"`
	// Stale marks an item whose newest value is older than three collection
	// intervals. It is only meaningful for items that collect on a schedule.
	Stale bool `json:"stale"`
	// ReadError records that this item's history could not be read. Without
	// it a failed read is indistinguishable from an item that collected
	// nothing — and in this tool "no data" reads as "your monitoring is
	// broken", which is a very different report to hand someone.
	ReadError string `json:"read_error,omitempty"`
}

// ItemQuery selects items on a host.
type ItemQuery struct {
	Host   string
	Search string
	Limit  int
	// EnabledOnly excludes items whose collection is switched off.
	EnabledOnly bool
}

const defaultItemLimit = 25

// noDataLookback bounds how far back a latest-value query scans. A value older
// than this is reported as no data rather than as a stale reading, which keeps
// the query from scanning an entire history table.
const noDataLookback = 7 * 24 * time.Hour

func (s *Service) resolveItems(ctx context.Context, hostID string, q ItemQuery) ([]wireItem, bool, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultItemLimit
	}
	params := map[string]any{
		"output":    itemOutputFields,
		"hostids":   []string{hostID},
		"sortfield": "name",
		"limit":     limit + 1,
	}
	applySearch(params, q.Search, "name", "key_")
	if q.EnabledOnly {
		params["filter"] = map[string]any{"status": "0"}
	}
	var wire []wireItem
	if err := s.client.CallIdempotent(ctx, "item.get", params, &wire); err != nil {
		return nil, false, errs.FromAPI(err)
	}
	kept, truncated := output.Bound(wire, limit)
	return kept, truncated, nil
}

// LatestValues returns the newest reading of each matching item.
//
// item.lastvalue and item.lastclock still appear in the API but have returned
// a constant "0" for several major releases, so the newest value has to come
// from history. history.get in turn needs an explicit history type: left
// unset it defaults to numeric unsigned and silently returns nothing for a
// float item, which is the single most common way this query goes wrong.
func (s *Service) LatestValues(ctx context.Context, q ItemQuery) (Host, []Value, bool, error) {
	host, err := s.ResolveHost(ctx, q.Host)
	if err != nil {
		return Host{}, nil, false, err
	}
	items, truncated, err := s.resolveItems(ctx, host.ID, q)
	if err != nil {
		return Host{}, nil, false, err
	}
	if len(items) == 0 {
		return host, []Value{}, false, nil
	}
	values, err := s.latestForItems(ctx, items)
	var partial errPartialHistory
	if errors.As(err, &partial) {
		// Some items answered and some did not. The values that arrived are
		// still worth reporting; the ones that did not carry their own
		// read_error.
		return host, values, truncated, nil
	}
	if err != nil {
		return Host{}, nil, false, err
	}
	return host, values, truncated, nil
}

func (s *Service) latestForItems(ctx context.Context, items []wireItem) ([]Value, error) {
	values := make([]Value, len(items))
	tasks := make([]task, 0, len(items))
	for i, w := range items {
		i, w := i, w
		values[i] = Value{Item: w.toItem(), NoData: true}
		if w.ValueType == valueTypeBinary {
			values[i].Value = "(binary)"
			values[i].NoData = false
			continue
		}
		tasks = append(tasks, task{
			name: w.Key,
			run: func(ctx context.Context) error {
				point, err := s.latestPoint(ctx, w)
				if err != nil {
					values[i].NoData = false
					values[i].ReadError = output.Sanitise(err.Error())
					return err
				}
				if point == nil {
					return nil
				}
				values[i] = *point
				return nil
			},
		})
	}
	failures := runParallel(ctx, tasks)
	if len(failures) == len(tasks) && len(tasks) > 0 {
		return nil, errs.New(errs.CodeAPI, errs.ExitAPI, "no item history could be read: %s", failures[0])
	}
	if len(failures) > 0 {
		return values, errPartialHistory{count: len(failures), first: failures[0]}
	}
	return values, nil
}

// errPartialHistory reports that some items could not be read while others
// could. It is not a failure of the whole call, so callers turn it into a
// warning rather than an error.
type errPartialHistory struct {
	count int
	first string
}

func (e errPartialHistory) Error() string {
	return fmt.Sprintf("%d item(s) could not be read: %s", e.count, e.first)
}

type wireHistory struct {
	ItemID string `json:"itemid"`
	Clock  string `json:"clock"`
	Value  string `json:"value"`
	NS     string `json:"ns"`
}

func (s *Service) latestPoint(ctx context.Context, w wireItem) (*Value, error) {
	params := map[string]any{
		"output":    "extend",
		"history":   atoi(w.ValueType),
		"itemids":   []string{w.ItemID},
		"sortfield": "clock",
		"sortorder": "DESC",
		"limit":     1,
		"time_from": time.Now().Add(-noDataLookback).Unix(),
	}
	var rows []wireHistory
	if err := s.client.CallIdempotent(ctx, "history.get", params, &rows); err != nil {
		return nil, errs.FromAPI(err)
	}
	v := Value{Item: w.toItem()}
	if len(rows) == 0 {
		v.NoData = true
		v.Stale = w.collectsOnSchedule()
		return &v, nil
	}
	row := rows[0]
	ts := unixToTime(row.Clock)
	v.Raw = output.Sanitise(row.Value)
	v.Value = FormatValue(row.Value, w.Units, w.ValueType)
	v.Timestamp = rfc3339(ts)
	if !ts.IsZero() {
		age := time.Since(ts)
		v.AgeSeconds = int64(age.Seconds())
		v.Age = HumanDuration(age)
		v.Stale = w.collectsOnSchedule() && age > 3*collectionInterval(w.Delay)
	}
	return &v, nil
}

// collectionInterval reads an item's update interval. Flexible and scheduling
// intervals are not parsed; they fall back to a conservative hour so that a
// complex schedule is never reported as stale on this evidence alone.
func collectionInterval(delay string) time.Duration {
	delay = strings.TrimSpace(delay)
	if delay == "" || strings.ContainsAny(delay, ";/") {
		return time.Hour
	}
	d, err := ParseWindow(delay)
	if err != nil || d <= 0 {
		return time.Hour
	}
	return d
}

// HistoryQuery selects a span of an item's history.
type HistoryQuery struct {
	Host   string
	Search string
	Window time.Duration
	Limit  int
	// Items bounds how many distinct items are read at once.
	Items int
}

// Series is one item's history over a window.
type Series struct {
	Item
	Points    []Point `json:"points"`
	Truncated bool    `json:"truncated"`
	ReadError string  `json:"read_error,omitempty"`
	// Summary carries min, max and average for numeric items.
	Summary *Summary `json:"summary,omitempty"`
}

// Point is one historical observation.
type Point struct {
	Timestamp string `json:"timestamp"`
	Value     string `json:"value"`
	Raw       string `json:"raw_value,omitempty"`
}

// Summary aggregates a numeric series, so an agent can reason about a window
// without reading every point.
type Summary struct {
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Avg   float64 `json:"avg"`
	Count int     `json:"count"`
	Units string  `json:"units,omitempty"`
}

const defaultHistoryPoints = 200

// History returns each matching item's values over the window.
func (s *Service) History(ctx context.Context, q HistoryQuery) (Host, []Series, error) {
	host, err := s.ResolveHost(ctx, q.Host)
	if err != nil {
		return Host{}, nil, err
	}
	itemLimit := q.Items
	if itemLimit <= 0 {
		itemLimit = 5
	}
	items, _, err := s.resolveItems(ctx, host.ID, ItemQuery{Search: q.Search, Limit: itemLimit})
	if err != nil {
		return Host{}, nil, err
	}
	if len(items) == 0 {
		return host, []Series{}, nil
	}
	window := q.Window
	if window <= 0 {
		window = time.Hour
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultHistoryPoints
	}

	series := make([]Series, len(items))
	tasks := make([]task, 0, len(items))
	for i, w := range items {
		i, w := i, w
		series[i] = Series{Item: w.toItem(), Points: []Point{}}
		if w.ValueType == valueTypeBinary {
			continue
		}
		tasks = append(tasks, task{
			name: w.Key,
			run: func(ctx context.Context) error {
				points, truncated, err := s.historyFor(ctx, w, window, limit)
				if err != nil {
					series[i].ReadError = output.Sanitise(err.Error())
					return err
				}
				series[i].Points = points
				series[i].Truncated = truncated
				series[i].Summary = summarise(points, w)
				return nil
			},
		})
	}
	if failures := runParallel(ctx, tasks); len(failures) == len(tasks) && len(tasks) > 0 {
		return Host{}, nil, errs.New(errs.CodeAPI, errs.ExitAPI, "no history could be read: %s", failures[0])
	}
	return host, series, nil
}

func (s *Service) historyFor(ctx context.Context, w wireItem, window time.Duration, limit int) ([]Point, bool, error) {
	params := map[string]any{
		"output":    "extend",
		"history":   atoi(w.ValueType),
		"itemids":   []string{w.ItemID},
		"time_from": time.Now().Add(-window).Unix(),
		"sortfield": "clock",
		"sortorder": "DESC",
		"limit":     limit + 1,
	}
	var rows []wireHistory
	if err := s.client.CallIdempotent(ctx, "history.get", params, &rows); err != nil {
		return nil, false, errs.FromAPI(err)
	}
	kept, truncated := output.Bound(rows, limit)
	// Zabbix returns newest first; a series reads better oldest first.
	sort.SliceStable(kept, func(i, j int) bool { return atoi64(kept[i].Clock) < atoi64(kept[j].Clock) })
	points := make([]Point, 0, len(kept))
	for _, r := range kept {
		points = append(points, Point{
			Timestamp: rfc3339(unixToTime(r.Clock)),
			Value:     FormatValue(r.Value, w.Units, w.ValueType),
			Raw:       output.Sanitise(r.Value),
		})
	}
	return points, truncated, nil
}

func summarise(points []Point, w wireItem) *Summary {
	if w.ValueType != valueTypeFloat && w.ValueType != valueTypeUint {
		return nil
	}
	if len(points) == 0 {
		return nil
	}
	sum := 0.0
	min, max := math.Inf(1), math.Inf(-1)
	n := 0
	for _, p := range points {
		f, ok := atof(p.Raw)
		if !ok {
			continue
		}
		n++
		sum += f
		min = math.Min(min, f)
		max = math.Max(max, f)
	}
	if n == 0 {
		return nil
	}
	return &Summary{Min: min, Max: max, Avg: sum / float64(n), Count: n, Units: w.Units}
}

// FormatValue renders a raw Zabbix value for a human reader, keeping the raw
// form alongside it so that an agent can still compute on the number.
func FormatValue(raw, units, valueType string) string {
	raw = output.Sanitise(raw)
	if valueType != valueTypeFloat && valueType != valueTypeUint {
		return raw
	}
	f, ok := atof(raw)
	if !ok {
		return raw
	}
	switch units {
	case "":
		return trimFloat(f)
	case "%":
		return fmt.Sprintf("%.2f%%", f)
	case "B":
		return humanBytes(f, "B")
	case "Bps":
		return humanBytes(f, "Bps")
	case "bps":
		return humanBytes(f, "bps")
	case "s":
		return HumanDuration(time.Duration(f * float64(time.Second)))
	case "uptime":
		return HumanDuration(time.Duration(f * float64(time.Second)))
	case "unixtime":
		// A zero here means the item never reported, not the start of 1970.
		if f <= 0 {
			return "never"
		}
		return rfc3339(time.Unix(int64(f), 0))
	default:
		return trimFloat(f) + " " + units
	}
}

func humanBytes(f float64, unit string) string {
	const step = 1024
	prefixes := []string{"", "K", "M", "G", "T", "P"}
	neg := f < 0
	if neg {
		f = -f
	}
	i := 0
	for f >= step && i < len(prefixes)-1 {
		f /= step
		i++
	}
	s := fmt.Sprintf("%.2f %s%s", f, prefixes[i], unit)
	if neg {
		return "-" + s
	}
	return s
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return fmt.Sprintf("%d", int64(f))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}
