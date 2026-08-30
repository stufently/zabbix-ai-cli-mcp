package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Approval records how a change came to be authorised.
type Approval string

const (
	// ApprovalCLIApply is a human running the command with --apply.
	ApprovalCLIApply Approval = "cli-apply"
	// ApprovalTerminal is a human running `approve` against a stored plan.
	ApprovalTerminal Approval = "approve"
	// ApprovalMCPWrite is an agent applying a change through the MCP write
	// tool, which configuration has allowed. Recorded distinctly from
	// ApprovalCLIApply so the log says whether a person or a model was at the
	// other end.
	ApprovalMCPWrite Approval = "mcp-write"
)

// AuditEntry is one executed write.
//
// The tool this replaces logged nothing when an agent bypassed it, which is
// the failure this file exists to prevent: a permitted path that is recorded
// beats a refusal that gets routed around.
type AuditEntry struct {
	Time      time.Time      `json:"time"`
	Profile   string         `json:"profile"`
	Operation string         `json:"operation"`
	Risk      Risk           `json:"risk"`
	PlanID    string         `json:"plan_id,omitempty"`
	Approval  Approval       `json:"approval"`
	Params    map[string]any `json:"params,omitempty"`
	Resources []Resource     `json:"resources,omitempty"`
	Outcome   string         `json:"outcome"`
	Error     string         `json:"error,omitempty"`
}

// AuditLog appends executed writes to a file.
type AuditLog struct{ path string }

// NewAuditLog prepares the audit log inside the state directory.
func NewAuditLog(stateDir string) (*AuditLog, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", stateDir, err)
	}
	return &AuditLog{path: filepath.Join(stateDir, "audit.log")}, nil
}

// Path reports the log location.
func (a *AuditLog) Path() string { return a.path }

// Append records one entry as a JSON line.
func (a *AuditLog) Append(e AuditEntry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(line, '\n'))
	return err
}

// Find returns the most recent audit entry for a plan, if the plan was ever
// executed. It lets a caller that only holds a plan identifier discover the
// outcome, which matters over MCP: the plan file is removed once applied, so
// its absence alone cannot distinguish "done" from "expired".
func (a *AuditLog) Find(planID string) (*AuditEntry, error) {
	data, err := os.ReadFile(a.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found *AuditEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.PlanID == planID {
			entry := e
			found = &entry
		}
	}
	return found, nil
}
