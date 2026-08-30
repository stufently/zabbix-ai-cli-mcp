package ops

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
)

func TestEveryOperationIsWellFormed(t *testing.T) {
	seenName := map[string]bool{}
	seenCLI := map[string]bool{}
	seenTool := map[string]bool{}

	for _, op := range All() {
		if op.Name == "" || len(op.CLI) == 0 {
			t.Errorf("operation %+v has no name or command path", op)
			continue
		}
		if seenName[op.Name] {
			t.Errorf("duplicate operation name %q", op.Name)
		}
		seenName[op.Name] = true

		if seenCLI[op.CommandPath()] {
			t.Errorf("duplicate command path %q", op.CommandPath())
		}
		seenCLI[op.CommandPath()] = true

		if op.MCPTool != "" {
			if seenTool[op.MCPTool] {
				t.Errorf("duplicate MCP tool name %q", op.MCPTool)
			}
			seenTool[op.MCPTool] = true
			if !strings.HasPrefix(op.MCPTool, "zabbix_") {
				t.Errorf("%s: MCP tool %q must be namespaced", op.Name, op.MCPTool)
			}
		}
		if op.Summary == "" {
			t.Errorf("%s has no summary; it becomes the tool description", op.Name)
		}
		if op.Run == nil && op.Plan == nil {
			t.Errorf("%s does nothing", op.Name)
		}
		if op.Scope == "" {
			t.Errorf("%s declares no scope", op.Name)
		}
		if err := config.ValidateScopes([]string{op.Scope}); err != nil {
			t.Errorf("%s declares an unknown scope %q", op.Name, op.Scope)
		}
		for _, p := range op.Params {
			if p.Description == "" {
				t.Errorf("%s: parameter %q has no description", op.Name, p.Name)
			}
			if strings.ToLower(p.Name) != p.Name {
				t.Errorf("%s: parameter %q must be lower case", op.Name, p.Name)
			}
		}
	}
}

func TestReadOperationsCannotWrite(t *testing.T) {
	for _, op := range All() {
		if op.Risk == safety.RiskRead && op.Plan != nil && op.IsWrite == nil {
			t.Errorf("%s is classed as a read but has a plan function and no per-call rule", op.Name)
		}
		if op.Risk != safety.RiskRead && op.Plan == nil {
			t.Errorf("%s is classed as %q but cannot produce a plan", op.Name, op.Risk)
		}
	}
}

func TestWriteOperationsRequireANonReadScope(t *testing.T) {
	for _, op := range Writable() {
		if op.Name == "api.call" {
			// The escape hatch takes its scope from the method it is handed.
			continue
		}
		if op.Scope == safety.ScopeRead {
			t.Errorf("%s writes but only needs the read scope", op.Name)
		}
	}
}

func TestSchemasAreGeneratedForEveryExposedTool(t *testing.T) {
	for _, op := range All() {
		if op.MCPTool == "" {
			continue
		}
		schema := op.InputSchema()
		if schema["type"] != "object" {
			t.Errorf("%s: schema type = %v", op.Name, schema["type"])
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("%s: schema has no properties", op.Name)
		}
	}
}

func TestScopeIsEnforced(t *testing.T) {
	readOnly := &opspec.Env{Profile: "prod", Config: config.Profile{}}
	granted := &opspec.Env{Profile: "prod", Config: config.Profile{Scopes: []string{config.ScopeMaintenance}}}

	create, ok := Lookup("maintenance.create")
	if !ok {
		t.Fatal("maintenance.create is missing from the registry")
	}
	if err := CheckScope(readOnly, create); err == nil {
		t.Error("a read-only profile must not be able to plan a maintenance window")
	}
	if err := CheckScope(granted, create); err != nil {
		t.Errorf("a profile with the maintenance scope must be allowed: %v", err)
	}

	list, _ := Lookup("problems.list")
	if err := CheckScope(readOnly, list); err != nil {
		t.Errorf("reads must never need a scope: %v", err)
	}
}

func TestWritableNamesCoverEveryPlanOperation(t *testing.T) {
	names := WritableNames()
	if len(names) == 0 {
		t.Fatal("no writable operations are registered")
	}
	for _, name := range names {
		op, ok := Lookup(name)
		if !ok || op.Plan == nil {
			t.Errorf("WritableNames lists %q, which cannot be planned", name)
		}
	}
}

func TestLookupCommandAcceptsBothForms(t *testing.T) {
	byPath, ok := LookupCommand("host investigate")
	if !ok {
		t.Fatal("lookup by command path failed")
	}
	byName, ok := LookupCommand("host.investigate")
	if !ok {
		t.Fatal("lookup by operation name failed")
	}
	if byPath.Name != byName.Name {
		t.Errorf("the two forms resolved differently: %s and %s", byPath.Name, byName.Name)
	}
}

func TestApproveCommandNamesTheTargetForDestructivePlans(t *testing.T) {
	plan, err := safety.NewPlan("maintenance.delete", "prod", safety.RiskDestructive, safety.ScopeMaintenance)
	if err != nil {
		t.Fatal(err)
	}
	plan.RequiresConfirmName = "weekend window"
	got := ApproveCommand(plan)
	if !strings.Contains(got, plan.ID) || !strings.Contains(got, `--confirm "weekend window"`) {
		t.Errorf("approve command = %q", got)
	}
}

// A stored plan is a file its author can rewrite, hash and all. Apply must
// therefore believe the registry over the file.
func TestATamperedPlanIsRefusedRatherThanApplied(t *testing.T) {
	env := &opspec.Env{
		Profile: "prod",
		Config:  config.Profile{Scopes: []string{config.ScopeMaintenance}},
	}

	// A destructive maintenance delete, rewritten to look like an ordinary
	// write so that it would skip the confirmation.
	plan, err := safety.NewPlan("maintenance.delete", "prod", safety.RiskWrite, safety.ScopeMaintenance)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	plan.Params = map[string]any{"maintenanceids": []any{"6"}}
	plan.Summary = "Delete maintenance window"
	if err := plan.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err = Apply(context.Background(), env, plan, ApplyOptions{Mode: ApplyStored, Approval: safety.ApprovalTerminal})
	if err == nil {
		t.Fatal("a plan understating its own risk was applied")
	}
	if !strings.Contains(err.Error(), "claims to be") {
		t.Errorf("error = %q, want it to name the disagreement", err)
	}
}

// The escape hatch shares one registry entry across every method, so its risk
// has to be recomputed from the method the plan carries.
func TestARawPlanIsClassifiedFromItsMethodNotItsFile(t *testing.T) {
	env := &opspec.Env{
		Profile: "prod",
		Config:  config.Profile{Scopes: []string{config.ScopeMaintenance}},
	}
	plan, err := safety.NewPlan("api.call", "prod", safety.RiskWrite, safety.ScopeMaintenance)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	// maintenance.delete is destructive, not an ordinary write.
	plan.Params = map[string]any{"method": "maintenance.delete", "params": []any{"6"}}
	if err := plan.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := Apply(context.Background(), env, plan, ApplyOptions{Mode: ApplyStored, Approval: safety.ApprovalTerminal}); err == nil {
		t.Fatal("a raw plan understating its own risk was applied")
	}
}

func TestApplyReportsBothAuditAndPlanCleanupFailures(t *testing.T) {
	stateDir := t.TempDir()
	store, err := safety.NewStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := safety.NewAuditLog(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	// A directory where audit.log should be makes Append fail reliably.
	if err := os.Mkdir(audit.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	plan, err := safety.NewPlan("api.call", "prod", safety.RiskWrite, safety.ScopeMaintenance)
	if err != nil {
		t.Fatal(err)
	}
	plan.Params = map[string]any{"method": "maintenance.create", "params": map[string]any{}}
	if err := plan.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(plan); err != nil {
		t.Fatal(err)
	}
	claimedPath := filepath.Join(store.Dir(), plan.ID+".json.claimed")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Claim has already renamed the plan. Replace the claimed file with a
		// non-empty directory, which Discard cannot remove on any platform.
		if err := os.Remove(claimedPath); err != nil {
			t.Errorf("remove claimed plan: %v", err)
		}
		if err := os.Mkdir(claimedPath, 0o700); err != nil {
			t.Errorf("replace claimed plan: %v", err)
		}
		if err := os.WriteFile(filepath.Join(claimedPath, "blocker"), []byte("x"), 0o600); err != nil {
			t.Errorf("make claimed directory non-empty: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","error":{"code":-32603,"message":"write failed"},"id":1}`)
	}))
	t.Cleanup(server.Close)
	env := &opspec.Env{
		Service: service.New(api.New(server.URL, "token")),
		Profile: "prod",
		Config:  config.Profile{Scopes: []string{config.ScopeMaintenance}},
		Plans:   store,
		Audit:   audit,
	}

	_, err = Apply(context.Background(), env, plan, ApplyOptions{Mode: ApplyStored, Approval: safety.ApprovalTerminal})
	if err == nil {
		t.Fatal("Apply unexpectedly succeeded")
	}
	for _, want := range []string{"could not be written to the audit log", "plan file could not be removed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Apply error %q does not contain %q", err, want)
		}
	}
}

func TestHistorySummaryShowsReadErrorInTable(t *testing.T) {
	got := historySummary(service.Series{ReadError: "history permission denied"})
	if !strings.Contains(got, "read error") || !strings.Contains(got, "permission denied") {
		t.Fatalf("history summary = %q", got)
	}
}

func TestAnUnstatedApplyModeIsRefused(t *testing.T) {
	// Reading an unset mode as the permissive one is how a gate quietly stops
	// being a gate.
	env := &opspec.Env{Profile: "test", AllowWrite: true}
	if err := CheckWriteAllowed(env, ""); err == nil {
		t.Fatal("a change that does not say how it was authorised must be refused")
	}
}

func TestDirectWritesAreRefusedWhenConfigurationSaysSo(t *testing.T) {
	env := &opspec.Env{Profile: "test"}
	if err := CheckWriteAllowed(env, ApplyDirect); err == nil {
		t.Fatal("a direct write must be refused when the setting is off")
	}
	// Approving a stored plan is a person at a terminal, and it is the path
	// the refusal above sends the caller to. Refusing it as well would leave
	// nothing that works.
	if err := CheckWriteAllowed(env, ApplyStored); err != nil {
		t.Errorf("approving a stored plan must stay available: %v", err)
	}
	env.AllowWrite = true
	if err := CheckWriteAllowed(env, ApplyDirect); err != nil {
		t.Errorf("a direct write must be allowed when the setting is on: %v", err)
	}
}
