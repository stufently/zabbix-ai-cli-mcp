// Package errs defines the error contract shared by the CLI, the MCP server and
// the JSON envelope. Every failure an agent can observe carries a stable code,
// a retryable flag and, where one exists, an actionable suggestion.
package errs

import (
	"context"
	"errors"
	"fmt"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
)

// Exit codes. Documented in docs/json-output.md and covered by CLI tests.
const (
	ExitOK               = 0
	ExitFailure          = 1
	ExitUsage            = 2
	ExitAuth             = 3
	ExitAPI              = 4
	ExitNotFound         = 5
	ExitPermission       = 6
	ExitApprovalRequired = 7
	ExitUnsupported      = 8
)

// Stable, machine-readable error codes.
const (
	CodeInternal         = "INTERNAL_ERROR"
	CodeUsage            = "INVALID_ARGUMENTS"
	CodeAuth             = "AUTHENTICATION_FAILED"
	CodeAPI              = "ZABBIX_API_ERROR"
	CodeConnection       = "CONNECTION_FAILED"
	CodeTimeout          = "TIMEOUT"
	CodeNotFound         = "NOT_FOUND"
	CodeHostNotFound     = "HOST_NOT_FOUND"
	CodePermission       = "PERMISSION_DENIED"
	CodeApprovalRequired = "APPROVAL_REQUIRED"
	CodeUnsupported      = "UNSUPPORTED_ZABBIX_VERSION"
	CodeDenied           = "OPERATION_DENIED"
	CodeScope            = "SCOPE_NOT_GRANTED"
	CodeWriteDisabled    = "WRITE_DISABLED"
	CodeNoProfile        = "NO_PROFILE"
	CodePlanExpired      = "PLAN_EXPIRED"
	CodePlanStale        = "PLAN_PRECONDITION_FAILED"
	CodePlanNotFound     = "PLAN_NOT_FOUND"
	CodeAmbiguous        = "AMBIGUOUS_MATCH"
)

// E is a structured, agent-facing error.
type E struct {
	Code       string
	Message    string
	Retryable  bool
	Suggestion string
	Exit       int
	wrapped    error
}

func (e *E) Error() string { return e.Message }
func (e *E) Unwrap() error { return e.wrapped }

// New builds an error with an explicit code and exit status.
func New(code string, exit int, format string, args ...any) *E {
	return &E{Code: code, Exit: exit, Message: fmt.Sprintf(format, args...)}
}

// WithSuggestion returns a copy carrying an actionable next step.
func (e *E) WithSuggestion(format string, args ...any) *E {
	c := *e
	c.Suggestion = fmt.Sprintf(format, args...)
	return &c
}

// Retry marks the error as worth retrying.
func (e *E) Retry() *E {
	c := *e
	c.Retryable = true
	return &c
}

// Wrap attaches an underlying cause without exposing it to the agent.
func (e *E) Wrap(err error) *E {
	c := *e
	c.wrapped = err
	return &c
}

// Usage reports a caller mistake: a missing argument, a bad value.
func Usage(format string, args ...any) *E { return New(CodeUsage, ExitUsage, format, args...) }

// NotFound reports a resource that does not exist.
func NotFound(format string, args ...any) *E { return New(CodeNotFound, ExitNotFound, format, args...) }

// HostNotFound is the specialised, most common not-found case.
func HostNotFound(name string) *E {
	return New(CodeHostNotFound, ExitNotFound, "host %q was not found", name).
		WithSuggestion("run 'zabbix-ai-cli-mcp host list --search %s' to find the exact name; matching is fuzzy", name)
}

// Ambiguous reports a fuzzy lookup that matched more than one resource.
func Ambiguous(format string, args ...any) *E {
	return New(CodeAmbiguous, ExitUsage, format, args...)
}

// Denied reports an operation refused by the risk registry.
func Denied(format string, args ...any) *E { return New(CodeDenied, ExitPermission, format, args...) }

// Internal reports a defect in this program.
func Internal(format string, args ...any) *E {
	return New(CodeInternal, ExitFailure, format, args...)
}

// ApprovalRequired reports a write that has been planned but not approved.
func ApprovalRequired(format string, args ...any) *E {
	return New(CodeApprovalRequired, ExitApprovalRequired, format, args...)
}

// Unsupported reports a capability missing from the connected Zabbix version.
func Unsupported(format string, args ...any) *E {
	return New(CodeUnsupported, ExitUnsupported, format, args...)
}

// FromAPI converts a client-level error into the agent-facing contract. It
// deliberately does not surface the raw transport error text for
// authentication failures, so a token can never travel in an error message.
func FromAPI(err error) *E {
	if err == nil {
		return nil
	}
	var e *E
	if errors.As(err, &e) {
		return e
	}

	var ae *api.APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Authentication():
			return New(CodeAuth, ExitAuth, "Zabbix rejected the configured API token").
				WithSuggestion("run 'zabbix-ai-cli-mcp login' to store a new token for the active profile").
				Wrap(err)
		case ae.Permission():
			return New(CodePermission, ExitPermission, "the Zabbix token lacks permission for this operation").
				WithSuggestion("grant the required role in Zabbix, or use a profile whose token has it").
				Wrap(err)
		}
		return New(CodeAPI, ExitAPI, "%s", ae.Message+details(ae.Data)).Wrap(err)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return New(CodeTimeout, ExitAPI, "the Zabbix API did not respond in time").
			WithSuggestion("narrow the query with --limit or --last, or raise --timeout").Retry().Wrap(err)
	}

	var te *api.TransportError
	if errors.As(err, &te) {
		return New(CodeConnection, ExitAPI, "could not reach the Zabbix API: %s", te.Err.Error()).Retry().Wrap(err)
	}
	if s := api.HTTPStatus(err); s > 0 {
		e := New(CodeConnection, ExitAPI, "the Zabbix endpoint returned HTTP %d", s)
		if s >= 500 {
			e = e.Retry()
		}
		return e.Wrap(err)
	}
	return New(CodeInternal, ExitFailure, "%s", err.Error()).Wrap(err)
}

func details(data string) string {
	if data == "" {
		return ""
	}
	return " " + data
}

// ExitCode reports the process exit status for err.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var e *E
	if errors.As(err, &e) && e.Exit != 0 {
		return e.Exit
	}
	return ExitFailure
}
