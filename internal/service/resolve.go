package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
)

// Patterns found in the notification templates Zabbix ships with, which is
// what an operator pastes out of a chat client.
var (
	reOriginalID  = regexp.MustCompile(`(?i)original\s+problem\s+id:?\s*(\d+)`)
	reEventID     = regexp.MustCompile(`(?i)\bevent\s*id:?\s*(\d+)`)
	reHostLine    = regexp.MustCompile(`(?im)^\s*host:?\s*(.+?)\s*$`)
	reProblemName = regexp.MustCompile(`(?im)^\s*problem\s+name:?\s*(.+?)\s*$`)
	reBareNumber  = regexp.MustCompile(`^\s*(\d{3,})\s*$`)
)

// Resolution is what a pasted alert was matched to.
type Resolution struct {
	// Input echoes the extracted fields, so an agent can see what was read
	// out of the text rather than guessing why a match failed.
	Extracted ExtractedAlert `json:"extracted"`
	Event     *EventSummary  `json:"event,omitempty"`
	Host      *Host          `json:"host,omitempty"`
	Problems  []Problem      `json:"problems,omitempty"`
	Findings  []string       `json:"findings"`
}

// ExtractedAlert holds the fields recognised in the pasted text.
type ExtractedAlert struct {
	EventID     string `json:"eventid,omitempty"`
	Host        string `json:"host,omitempty"`
	ProblemName string `json:"problem_name,omitempty"`
}

// ResolveAlertText turns the text of a notification into the identifiers the
// other commands need.
//
// Operators paste alerts out of chat; without this, an instruction like
// "acknowledge that one" cannot be acted on at all.
func (s *Service) ResolveAlertText(ctx context.Context, text string) (*Resolution, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errs.Usage("no alert text was given")
	}
	res := &Resolution{}
	ex := &res.Extracted

	if m := reBareNumber.FindStringSubmatch(text); m != nil {
		ex.EventID = m[1]
	}
	if ex.EventID == "" {
		if m := reOriginalID.FindStringSubmatch(text); m != nil {
			ex.EventID = m[1]
		}
	}
	if ex.EventID == "" {
		if m := reEventID.FindStringSubmatch(text); m != nil {
			ex.EventID = m[1]
		}
	}
	if m := reHostLine.FindStringSubmatch(text); m != nil {
		ex.Host = output.Sanitise(strings.TrimSpace(m[1]))
	}
	if m := reProblemName.FindStringSubmatch(text); m != nil {
		ex.ProblemName = output.Sanitise(strings.TrimSpace(m[1]))
	}

	if ex.EventID != "" {
		ev, err := s.getEvent(ctx, ex.EventID)
		if err == nil {
			sum := ev.summary()
			res.Event = &sum
			res.Findings = append(res.Findings, "matched event "+sum.EventID+" by its identifier")
			if len(sum.Hosts) > 0 {
				if h, err := s.ResolveHost(ctx, sum.Hosts[0].ID); err == nil {
					res.Host = &h
				}
			}
			return res, nil
		}
		res.Findings = append(res.Findings, "the text names event "+ex.EventID+", but no such event exists")
	}

	if ex.Host == "" && ex.ProblemName == "" {
		return nil, errs.NotFound("no host, problem name or event identifier could be read from the text").
			WithSuggestion("paste the whole notification, or pass the event ID directly")
	}

	if ex.Host != "" {
		h, err := s.ResolveHost(ctx, ex.Host)
		if err == nil {
			res.Host = &h
			problems, _, err := s.ListProblems(ctx, ProblemQuery{Host: h.ID, Limit: 50})
			if err != nil {
				return nil, err
			}
			res.Problems = filterByName(problems, ex.ProblemName)
			if len(res.Problems) == 1 {
				res.Findings = append(res.Findings,
					"matched one active problem on "+h.Name+" by name")
			} else if len(res.Problems) == 0 {
				res.Findings = append(res.Findings,
					"host "+h.Name+" has no matching active problem; it may already be resolved")
			}
			return res, nil
		}
		res.Findings = append(res.Findings, "no host matches "+ex.Host)
	}

	problems, _, err := s.ListProblems(ctx, ProblemQuery{Limit: 100})
	if err != nil {
		return nil, err
	}
	res.Problems = filterByName(problems, ex.ProblemName)
	if len(res.Problems) == 0 {
		res.Findings = append(res.Findings, "no active problem matches the pasted text")
	}
	return res, nil
}

func filterByName(problems []Problem, name string) []Problem {
	if name == "" {
		return problems
	}
	needle := strings.ToLower(name)
	var exact, partial []Problem
	for _, p := range problems {
		lower := strings.ToLower(p.Name)
		switch {
		case lower == needle:
			exact = append(exact, p)
		case strings.Contains(lower, needle) || strings.Contains(needle, lower):
			partial = append(partial, p)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}
