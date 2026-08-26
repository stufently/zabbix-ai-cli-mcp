// Package safety implements the boundary between an agent's intent and a
// change to Zabbix.
//
// The model has one rule: nothing that writes executes on the same call that
// requested it. A CLI user confirms with --apply, because a human typed the
// command. An MCP client cannot confirm at all; it receives a plan identifier
// and the change happens only when a person runs `zabbix-ai-cli-mcp approve`.
// A confirmation token carried inside the MCP channel would be approved by the
// same model that asked for it, which is why one is not offered.
package safety

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Risk classifies what an operation can do.
type Risk string

const (
	// RiskRead cannot change anything and runs immediately.
	RiskRead Risk = "read"
	// RiskWrite changes configuration or state and needs a plan.
	RiskWrite Risk = "write"
	// RiskDestructive removes something that cannot be recovered from Zabbix
	// itself, and needs the object named back on the command line.
	RiskDestructive Risk = "destructive"
)

// DefaultTTL bounds how long a plan may sit unapproved. Long enough for a
// person to read it, short enough that the world has not moved on.
const DefaultTTL = 15 * time.Minute

// Resource is an object a plan will act on.
type Resource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Change is one line of the plan's diff.
type Change struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Precondition is a read that must still hold when the plan is applied.
//
// Re-checking before execution is what separates a plan from a stale
// instruction: if the maintenance window a plan meant to delete has already
// been replaced by a different one with the same ID, applying blind would
// destroy the wrong thing.
type Precondition struct {
	Description string         `json:"description"`
	Method      string         `json:"method"`
	Params      map[string]any `json:"params"`
	ExpectCount *int           `json:"expect_count,omitempty"`
	ExpectField map[string]any `json:"expect_field,omitempty"`
}

// Plan is a change that has been described but not made.
type Plan struct {
	ID            string         `json:"id"`
	Operation     string         `json:"operation"`
	Profile       string         `json:"profile"`
	Risk          Risk           `json:"risk"`
	Scope         string         `json:"scope"`
	Summary       string         `json:"summary"`
	Changes       []Change       `json:"changes,omitempty"`
	Params        map[string]any `json:"params"`
	Resources     []Resource     `json:"resources,omitempty"`
	ImpactCount   int            `json:"impact_count"`
	Preconditions []Precondition `json:"preconditions,omitempty"`
	Hash          string         `json:"hash"`
	CreatedAt     time.Time      `json:"created_at"`
	ExpiresAt     time.Time      `json:"expires_at"`
	// RequiresConfirmName holds the exact string a destructive plan must have
	// echoed back on the command line.
	RequiresConfirmName string `json:"requires_confirm_name,omitempty"`
}

// NewPlan builds a plan and stamps it with an identifier, a hash and a
// deadline.
func NewPlan(operation, profile string, risk Risk, scope string) (*Plan, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &Plan{
		ID:        id,
		Operation: operation,
		Profile:   profile,
		Risk:      risk,
		Scope:     scope,
		Params:    map[string]any{},
		CreatedAt: now,
		ExpiresAt: now.Add(DefaultTTL),
	}, nil
}

// Seal computes the plan's fingerprint. It must be called once the plan is
// final, and it is verified again before execution.
func (p *Plan) Seal() error {
	h, err := p.fingerprint()
	if err != nil {
		return err
	}
	p.Hash = h
	return nil
}

// Verify reports whether the plan is still internally consistent and unexpired.
func (p *Plan) Verify(now time.Time) error {
	h, err := p.fingerprint()
	if err != nil {
		return err
	}
	if h != p.Hash {
		return fmt.Errorf("plan %s has been modified since it was created", p.ID)
	}
	if now.After(p.ExpiresAt) {
		return fmt.Errorf("plan %s expired at %s", p.ID, p.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

// Expired reports whether the plan is past its deadline.
func (p *Plan) Expired(now time.Time) bool { return now.After(p.ExpiresAt) }

// Describe renders the plan the way it is shown before approval.
func (p *Plan) Describe() string {
	out := fmt.Sprintf("PLAN %s\n\n%s\n", p.ID, p.Summary)
	if len(p.Resources) > 0 {
		out += "\nAffects:\n"
		for _, r := range p.Resources {
			name := r.Name
			if name == "" {
				name = r.ID
			}
			out += fmt.Sprintf("  %s %s\n", r.Kind, name)
		}
	}
	if len(p.Changes) > 0 {
		out += "\nChanges:\n"
		for _, c := range p.Changes {
			switch {
			case c.Before == "":
				out += fmt.Sprintf("  %s: %s\n", c.Field, c.After)
			case c.After == "":
				out += fmt.Sprintf("  %s: %s (removed)\n", c.Field, c.Before)
			default:
				out += fmt.Sprintf("  %s: %s -> %s\n", c.Field, c.Before, c.After)
			}
		}
	}
	out += fmt.Sprintf("\nRisk: %s\nExpires: %s\n", p.Risk, p.ExpiresAt.Format(time.RFC3339))
	return out
}

func newID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "pl_" + hex.EncodeToString(b[:]), nil
}

// fingerprint hashes the whole plan apart from the hash itself.
//
// Covering only the parameters would leave the deadline, the risk class, the
// confirmation requirement, the preconditions and the summary editable on
// disk. The summary matters as much as the parameters: it is the text a person
// reads before approving, and a plan that describes one change while carrying
// another is worse than no plan at all.
//
// A plan is stored as a file owned by the same user the agent runs as, so
// "nothing can edit it" is not an assumption worth resting on.
//
// json.Marshal sorts map keys and renders integral floats without a decimal
// point, so the encoding survives the round trip through the plan file.
func (p *Plan) fingerprint() (string, error) {
	clone := *p
	clone.Hash = ""
	payload, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
