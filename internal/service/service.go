// Package service holds the domain use cases. Both the CLI and the MCP server
// call into this package and nothing else, so the two front ends cannot drift
// apart in behaviour.
//
// Every function here takes a context, bounds its own result, and prefers a
// partial answer over a failed one: an agent diagnosing an incident is better
// served by four facts out of five plus a warning than by an error.
package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/zbx"
)

// Service exposes the domain use cases over a Zabbix API client.
type Service struct {
	client *api.Client

	mu      sync.Mutex
	version *zbx.Version
}

// New returns a Service backed by client.
func New(client *api.Client) *Service { return &Service{client: client} }

// Client exposes the underlying API client for the raw escape hatch.
func (s *Service) Client() *api.Client { return s.client }

// Version returns the connected server's API version, fetched once per
// process. apiinfo.version requires no authentication, which makes it a safe
// connectivity probe.
func (s *Service) Version(ctx context.Context) (zbx.Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version != nil {
		return *s.version, nil
	}
	var raw string
	if err := s.client.CallIdempotent(ctx, "apiinfo.version", map[string]any{}, &raw); err != nil {
		return zbx.Version{}, errs.FromAPI(err)
	}
	v, err := zbx.ParseVersion(raw)
	if err != nil {
		return zbx.Version{}, errs.Internal("%v", err)
	}
	s.version = &v
	return v, nil
}

// RequireCapability fails with a clear, actionable error when the connected
// server is too old for an operation, rather than letting it return nothing.
func (s *Service) RequireCapability(ctx context.Context, c zbx.Capability) error {
	v, err := s.Version(ctx)
	if err != nil {
		return err
	}
	if v.Supports(c) {
		return nil
	}
	return errs.Unsupported("this operation needs Zabbix %s; the server reports %s",
		zbx.Requirement(c), v.String())
}

// maxConcurrency bounds the fan-out of an aggregate. Four keeps a single
// investigate from behaving like a load test against a small installation.
const maxConcurrency = 4

// task is one leg of an aggregate.
type task struct {
	name string
	run  func(context.Context) error
}

// runParallel executes tasks with bounded concurrency and collects failures
// per task instead of abandoning the whole aggregate.
func runParallel(ctx context.Context, tasks []task) []string {
	if len(tasks) == 0 {
		return nil
	}
	var mu sync.Mutex
	var failures []string
	var wg sync.WaitGroup
	jobs := make(chan task, len(tasks))
	for _, t := range tasks {
		jobs <- t
	}
	close(jobs)
	workers := min(maxConcurrency, len(tasks))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				select {
				case <-ctx.Done():
					mu.Lock()
					failures = append(failures, t.name+": cancelled")
					mu.Unlock()
					continue
				default:
				}
				if err := t.run(ctx); err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %v", t.name, err))
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	sort.Strings(failures)
	return failures
}

// Wire-level helpers. Zabbix returns identifiers, timestamps and numbers as
// strings, so every numeric field arrives as text and must be converted here
// rather than at each call site.

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func atof(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// unixToTime converts a Zabbix clock field, returning the zero time for "0".
func unixToTime(s string) time.Time {
	n := atoi64(s)
	if n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// rfc3339 formats a Zabbix clock for output. Times are always UTC and always
// carry their offset, so an agent never has to guess a timezone.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ParseWindow reads a duration written the way operators write one: 30m, 1h,
// 24h, 7d, 2w. Go's own syntax has no day or week unit, and asking an agent
// for 168h instead of 7d invites arithmetic mistakes.
func ParseWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errs.Usage("empty duration")
	}
	mult := time.Duration(0)
	switch {
	case strings.HasSuffix(s, "d"):
		mult = 24 * time.Hour
	case strings.HasSuffix(s, "w"):
		mult = 7 * 24 * time.Hour
	}
	if mult != 0 {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, s[len(s)-1:]), 64)
		if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) ||
			n > float64(math.MaxInt64)/float64(mult) {
			return 0, errs.Usage("invalid duration %q; use forms such as 30m, 2h, 7d, 2w", s)
		}
		d := time.Duration(n * float64(mult))
		if d <= 0 {
			return 0, errs.Usage("invalid duration %q; use forms such as 30m, 2h, 7d, 2w", s)
		}
		return d, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errs.Usage("invalid duration %q; use forms such as 30m, 2h, 7d, 2w", s)
	}
	return d, nil
}

// HumanDuration renders a span the way an operator reads one.
func HumanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// Severity names, indexed by the Zabbix numeric severity.
var severityNames = []string{"not classified", "information", "warning", "average", "high", "disaster"}

// SeverityName renders a Zabbix severity.
func SeverityName(s string) string {
	n := atoi(s)
	if n < 0 || n >= len(severityNames) {
		return "unknown"
	}
	return severityNames[n]
}

// SeverityValue parses a severity written as a name or a number.
func SeverityValue(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errs.Usage("empty severity")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > 5 {
			return 0, errs.Usage("severity %d is out of range; use 0 to 5", n)
		}
		return n, nil
	}
	aliases := map[string]int{
		"notclassified": 0, "not_classified": 0, "none": 0,
		"info": 1, "information": 1,
		"warning": 2, "warn": 2,
		"average": 3, "avg": 3,
		"high":     4,
		"disaster": 5, "critical": 5,
	}
	if n, ok := aliases[strings.ReplaceAll(s, " ", "")]; ok {
		return n, nil
	}
	return 0, errs.Usage("unknown severity %q; use a number 0-5 or one of: %s",
		s, strings.Join(severityNames, ", "))
}

// applySearch sets the search parameters for a pattern.
//
// Wildcards are enabled only when the pattern actually contains one. With
// searchWildcardsEnabled set, Zabbix stops wrapping the value in implicit
// wildcards and matches the string exactly, so leaving it permanently on turns
// every substring search into an exact-match search that quietly returns
// nothing.
func applySearch(params map[string]any, pattern string, fields ...string) {
	if pattern == "" {
		return
	}
	search := make(map[string]any, len(fields))
	for _, f := range fields {
		search[f] = pattern
	}
	params["search"] = search
	params["searchByAny"] = true
	if strings.Contains(pattern, "*") {
		params["searchWildcardsEnabled"] = true
	}
}
