package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	zmcp "github.com/stufently/zabbix-ai-cli-mcp/internal/mcp"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/zbxtest"
)

const testToken = "mcp-secret-token"

type harness struct {
	server  *zbxtest.Server
	session *sdk.ClientSession
	plans   *safety.Store
	audit   *safety.AuditLog
}

func newHarness(t *testing.T, readOnly bool, scopes ...string) *harness {
	t.Helper()
	return newHarnessWith(t, readOnly, false, false, scopes...)
}

// newWritableHarness runs the server the way configuration allowing direct
// writes runs it.
func newWritableHarness(t *testing.T, scopes ...string) *harness {
	t.Helper()
	return newHarnessWith(t, false, true, true, scopes...)
}

// newHarnessWith separates what the server was told at startup from what the
// environment answers per call, so the case where configuration was tightened
// after the tool list was built can be exercised.
func newHarnessWith(t *testing.T, readOnly, registerWrite, envAllowsWrite bool, scopes ...string) *harness {
	t.Helper()
	srv := zbxtest.New(t, "7.4.10")
	stateDir := t.TempDir()
	plans, err := safety.NewStore(stateDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	audit, err := safety.NewAuditLog(stateDir)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}

	server := zmcp.NewServer(zmcp.Options{
		Version:    "test",
		ReadOnly:   readOnly,
		AllowWrite: registerWrite,
		EnvFor: func(context.Context) (*opspec.Env, error) {
			return &opspec.Env{
				Service:    service.New(api.New(srv.URL, testToken)),
				Profile:    "test",
				Config:     config.Profile{URL: srv.URL, Scopes: scopes},
				Plans:      plans,
				Audit:      audit,
				AllowWrite: envAllowsWrite,
			}, nil
		},
	})

	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &harness{server: srv, session: session, plans: plans, audit: audit}
}

func (h *harness) call(t *testing.T, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: name, Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func text(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func envelope(t *testing.T, res *sdk.CallToolResult) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(text(t, res)), &env); err != nil {
		t.Fatalf("tool output is not JSON: %v\n%s", err, text(t, res))
	}
	return env
}

func TestToolSurfaceIsSmallAndNamespaced(t *testing.T) {
	h := newHarness(t, false)
	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
		if !strings.HasPrefix(tool.Name, "zabbix_") {
			t.Errorf("tool %q is not namespaced", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
	}
	// A large tool surface costs an agent context before it does anything at
	// all; the whole point of this design is a curated handful.
	if len(res.Tools) > 20 {
		t.Errorf("the server exposes %d tools; the design calls for a small set", len(res.Tools))
	}
	for _, required := range []string{
		"zabbix_problems", "zabbix_hosts", "zabbix_host_investigate",
		"zabbix_alert_why", "zabbix_resolve", "zabbix_api_call",
		"zabbix_plan_create", "zabbix_plan_status",
	} {
		if !names[required] {
			t.Errorf("tool %q is missing", required)
		}
	}
}

func TestNoToolChangesZabbixWhenDirectWritesAreOff(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	// Every tool is annotated read-only because none of them writes: the
	// planning tool only records an intention for a person to approve.
	for _, tool := range res.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not annotated as read-only", tool.Name)
		}
		schema, _ := json.Marshal(tool.InputSchema)
		for _, forbidden := range []string{`"apply"`, `"confirm"`, `"force"`} {
			if strings.Contains(string(schema), forbidden) {
				t.Errorf("tool %q accepts %s; MCP clients must not be able to authorise a change",
					tool.Name, forbidden)
			}
		}
	}
}

func TestReadToolReturnsTheEnvelope(t *testing.T) {
	h := newHarness(t, false)
	h.server.Reply("problem.get", []any{zbxtest.Problem("100", "500", "Disk full", "4", nil)})
	h.server.Reply("trigger.get", []any{map[string]any{
		"triggerid": "500", "hosts": []any{map[string]any{"hostid": "10", "name": "db01"}},
	}})

	res := h.call(t, "zabbix_problems", map[string]any{"limit": 10})
	if res.IsError {
		t.Fatalf("tool reported an error: %s", text(t, res))
	}
	env := envelope(t, res)
	if env["ok"] != true {
		t.Fatalf("ok = %v", env["ok"])
	}
	if res.StructuredContent == nil {
		t.Error("structured content is missing")
	}
}

func TestUnknownParameterIsRefusedWithTheAcceptedOnes(t *testing.T) {
	h := newHarness(t, false)
	res := h.call(t, "zabbix_problems", map[string]any{"selectHosts": "extend"})
	if !res.IsError {
		t.Fatal("an unknown parameter must be refused")
	}
	body := text(t, res)
	if !strings.Contains(body, "severity") || !strings.Contains(body, "selectHosts") {
		t.Errorf("the error must name the accepted parameters: %s", body)
	}
}

func TestPlanToolDescribesButDoesNotApply(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	h.server.Reply("maintenance.create", map[string]any{"maintenanceids": []any{"1"}})

	res := h.call(t, "zabbix_plan_create", map[string]any{
		"operation": "maintenance.create",
		"params":    map[string]any{"hosts": []any{"web01"}, "for": "2h"},
	})
	if res.IsError {
		t.Fatalf("plan creation failed: %s", text(t, res))
	}
	data := envelope(t, res)["data"].(map[string]any)
	if data["status"] != "planned" {
		t.Errorf("status = %v", data["status"])
	}
	approve, _ := data["approve_command"].(string)
	if !strings.HasPrefix(approve, "zabbix-ai-cli-mcp approve pl_") {
		t.Errorf("approve command = %q", approve)
	}
	if calls := h.server.CallsTo("maintenance.create"); len(calls) != 0 {
		t.Fatal("the planning tool changed Zabbix")
	}
}

func TestPlanToolRefusesAnOperationOutsideTheProfileScope(t *testing.T) {
	h := newHarness(t, false) // read-only profile
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})

	res := h.call(t, "zabbix_plan_create", map[string]any{
		"operation": "maintenance.create",
		"params":    map[string]any{"hosts": []any{"web01"}, "for": "2h"},
	})
	if !res.IsError {
		t.Fatal("a read-only profile must not be able to plan a write")
	}
	if !strings.Contains(text(t, res), "SCOPE_NOT_GRANTED") {
		t.Errorf("error = %s", text(t, res))
	}
}

func TestPlanToolIsAbsentInReadOnlyMode(t *testing.T) {
	h := newHarness(t, true, config.ScopeMaintenance)
	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		if strings.HasPrefix(tool.Name, "zabbix_plan") || tool.Name == "zabbix_write" {
			t.Errorf("read-only mode still exposes %q", tool.Name)
		}
	}
}

func TestWriteToolAppearsOnlyWhenConfigurationAllowsIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		harness  func(*testing.T, ...string) *harness
		expected bool
	}{
		{"writes allowed", func(t *testing.T, s ...string) *harness { return newWritableHarness(t, s...) }, true},
		{"writes disabled", func(t *testing.T, s ...string) *harness { return newHarness(t, false, s...) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.harness(t, config.ScopeMaintenance)
			res, err := h.session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			found := false
			planning := false
			for _, tool := range res.Tools {
				switch tool.Name {
				case "zabbix_write":
					found = true
					if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
						t.Error("zabbix_write must not be annotated read-only")
					}
				case "zabbix_plan_create":
					planning = true
				}
			}
			if found != tc.expected {
				t.Errorf("zabbix_write present = %v, want %v", found, tc.expected)
			}
			// Describing a change without making it stays available either
			// way: it is how an agent shows an operator what it intends.
			if !planning {
				t.Error("zabbix_plan_create must be offered regardless of the write setting")
			}
		})
	}
}

func TestWriteToolAppliesTheChangeAndAuditsIt(t *testing.T) {
	h := newWritableHarness(t, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	h.server.Reply("maintenance.create", map[string]any{"maintenanceids": []any{"1"}})

	res := h.call(t, "zabbix_write", map[string]any{
		"operation": "maintenance.create",
		"params":    map[string]any{"hosts": []any{"web01"}, "for": "2h"},
	})
	if res.IsError {
		t.Fatalf("zabbix_write failed: %s", text(t, res))
	}
	if calls := h.server.CallsTo("maintenance.create"); len(calls) != 1 {
		t.Fatalf("maintenance.create was called %d times", len(calls))
	}
	if !strings.Contains(text(t, res), "applied") {
		t.Errorf("result does not report the change: %s", text(t, res))
	}
	planID := envelope(t, res)["data"].(map[string]any)["plan_id"].(string)
	entry, err := h.audit.Find(planID)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if entry == nil {
		t.Fatal("the change was not written to the audit log")
	}
	if entry.Approval != safety.ApprovalMCPWrite {
		t.Errorf("approval = %q, want %q", entry.Approval, safety.ApprovalMCPWrite)
	}
}

func TestWriteToolRecheckesTheSettingAtExecution(t *testing.T) {
	// The tool list was built at startup. Configuration tightened afterwards
	// must still be obeyed, rather than the stale registration deciding.
	h := newHarnessWith(t, false, true, false, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	h.server.Reply("maintenance.create", map[string]any{"maintenanceids": []any{"1"}})

	res := h.call(t, "zabbix_write", map[string]any{
		"operation": "maintenance.create",
		"params":    map[string]any{"hosts": []any{"web01"}, "for": "2h"},
	})
	if !res.IsError || !strings.Contains(text(t, res), "WRITE_DISABLED") {
		t.Fatalf("expected a refusal, got: %s", text(t, res))
	}
	if calls := h.server.CallsTo("maintenance.create"); len(calls) != 0 {
		t.Fatal("the change reached Zabbix after the setting was turned off")
	}
}

func TestPlanStatusReportsTheOutcome(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})

	created := h.call(t, "zabbix_plan_create", map[string]any{
		"operation": "maintenance.create",
		"params":    map[string]any{"hosts": []any{"web01"}, "for": "2h"},
	})
	planID := envelope(t, created)["data"].(map[string]any)["plan_id"].(string)

	res := h.call(t, "zabbix_plan_status", map[string]any{"plan_id": planID})
	data := envelope(t, res)["data"].(map[string]any)
	if data["status"] != "awaiting approval" {
		t.Errorf("status = %v", data["status"])
	}

	res = h.call(t, "zabbix_plan_status", map[string]any{"plan_id": "pl_ffffffffffff"})
	data = envelope(t, res)["data"].(map[string]any)
	if data["status"] != "gone" {
		t.Errorf("status for an unknown plan = %v", data["status"])
	}
}

func TestPlanStatusReportsCorruptPlanInsteadOfCallingItGone(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	path := filepath.Join(h.plans.Dir(), "pl_aaaaaaaaaaaa.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := h.call(t, "zabbix_plan_status", map[string]any{"plan_id": "pl_aaaaaaaaaaaa"})
	if !res.IsError {
		t.Fatal("corrupt plan was reported as a normal status")
	}
	if !strings.Contains(text(t, res), "corrupt") {
		t.Fatalf("error does not explain the corrupt plan: %s", text(t, res))
	}
}

func TestPlanStatusReportsExpiredAndClaimedPlans(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)

	expired, err := safety.NewPlan("maintenance.create", "test", safety.RiskWrite, safety.ScopeMaintenance)
	if err != nil {
		t.Fatal(err)
	}
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	expired.Summary = "expired plan"
	if err := expired.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := h.plans.Save(expired); err != nil {
		t.Fatal(err)
	}
	res := h.call(t, "zabbix_plan_status", map[string]any{"plan_id": expired.ID})
	data := envelope(t, res)["data"].(map[string]any)
	if data["status"] != "expired" {
		t.Fatalf("expired plan status = %v", data["status"])
	}
	if _, ok := data["approve_command"]; ok {
		t.Fatal("expired plan still advertises an approve command")
	}

	claimed, err := safety.NewPlan("maintenance.create", "test", safety.RiskWrite, safety.ScopeMaintenance)
	if err != nil {
		t.Fatal(err)
	}
	if err := claimed.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := h.plans.Save(claimed); err != nil {
		t.Fatal(err)
	}
	if err := h.plans.Claim(claimed.ID); err != nil {
		t.Fatal(err)
	}
	res = h.call(t, "zabbix_plan_status", map[string]any{"plan_id": claimed.ID})
	data = envelope(t, res)["data"].(map[string]any)
	if data["status"] != "applying" {
		t.Fatalf("claimed plan status = %v", data["status"])
	}
}

func TestPlanStatusReturnsAuditReadErrors(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	if err := os.Mkdir(h.audit.Path(), 0o700); err != nil {
		t.Fatal(err)
	}

	res := h.call(t, "zabbix_plan_status", map[string]any{"plan_id": "pl_ffffffffffff"})
	if !res.IsError {
		t.Fatal("audit read failure was reported as a normal plan status")
	}
	if strings.Contains(text(t, res), `"status": "gone"`) {
		t.Fatalf("audit read failure was masked as gone: %s", text(t, res))
	}
}

func TestApiCallToolIsReadOnly(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{map[string]any{"hostid": "1"}})

	res := h.call(t, "zabbix_api_call", map[string]any{"method": "host.get"})
	if res.IsError {
		t.Fatalf("a read method must work: %s", text(t, res))
	}

	res = h.call(t, "zabbix_api_call", map[string]any{
		"method": "maintenance.delete", "params": `["7"]`,
	})
	if !res.IsError {
		t.Fatal("the escape hatch must not perform writes over MCP")
	}
	if !strings.Contains(text(t, res), "zabbix_plan_create") {
		t.Errorf("the refusal should point at the planning tool: %s", text(t, res))
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 0 {
		t.Fatal("a write reached Zabbix through the escape hatch")
	}
}

func TestToolOutputNeverCarriesTheToken(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	h.server.Reply("problem.get", []any{})
	h.server.Reply("trigger.get", []any{})
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})

	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"zabbix_problems", map[string]any{}},
		{"zabbix_hosts", map[string]any{}},
		{"zabbix_api_call", map[string]any{"method": "host.get"}},
		{"zabbix_plan_create", map[string]any{
			"operation": "maintenance.create",
			"params":    map[string]any{"hosts": []any{"web01"}, "for": "2h"},
		}},
	} {
		res := h.call(t, call.name, call.args)
		if strings.Contains(text(t, res), testToken) {
			t.Errorf("%s leaked the Zabbix token to the MCP client", call.name)
		}
	}
}

func TestErrorsAreReportedInsideTheResult(t *testing.T) {
	h := newHarness(t, false)
	h.server.Fail("problem.get", -32602, "Invalid params.", "Not authorised.")

	res := h.call(t, "zabbix_problems", map[string]any{})
	if !res.IsError {
		t.Fatal("a failure must be marked as an error result")
	}
	env := envelope(t, res)
	if env["ok"] != false {
		t.Errorf("ok = %v", env["ok"])
	}
	body := env["error"].(map[string]any)
	if body["code"] != "AUTHENTICATION_FAILED" {
		t.Errorf("code = %v", body["code"])
	}
}

// Discarding a claim can fail after the change was applied, and a leftover
// claim outlives two whole TTLs. Once the audit log records an outcome, that
// is the answer — otherwise the tool tells the caller to "check again" for
// something that has already happened.
func TestPlanStatusPrefersTheAuditedOutcomeOverALeftoverClaim(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	plan, err := safety.NewPlan("maintenance.create", "test", safety.RiskWrite, safety.ScopeMaintenance)
	if err != nil {
		t.Fatal(err)
	}
	plan.Summary = "Create maintenance window"
	if err := plan.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := h.plans.Save(plan); err != nil {
		t.Fatal(err)
	}
	if err := h.plans.Claim(plan.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.audit.Append(safety.AuditEntry{
		Profile:   "test",
		Operation: "maintenance.create",
		Risk:      safety.RiskWrite,
		PlanID:    plan.ID,
		Outcome:   "applied",
	}); err != nil {
		t.Fatal(err)
	}

	res := h.call(t, "zabbix_plan_status", map[string]any{"plan_id": plan.ID})
	data := envelope(t, res)["data"].(map[string]any)
	if data["status"] != "applied" {
		t.Errorf("status = %v, want the audited outcome", data["status"])
	}
}

// A claim with no audit entry still means the change is in flight.
func TestPlanStatusReportsAnUnauditedClaimAsApplying(t *testing.T) {
	h := newHarness(t, false, config.ScopeMaintenance)
	plan, err := safety.NewPlan("maintenance.create", "test", safety.RiskWrite, safety.ScopeMaintenance)
	if err != nil {
		t.Fatal(err)
	}
	plan.Summary = "Create maintenance window"
	if err := plan.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := h.plans.Save(plan); err != nil {
		t.Fatal(err)
	}
	if err := h.plans.Claim(plan.ID); err != nil {
		t.Fatal(err)
	}

	res := h.call(t, "zabbix_plan_status", map[string]any{"plan_id": plan.ID})
	data := envelope(t, res)["data"].(map[string]any)
	if data["status"] != "applying" {
		t.Errorf("status = %v, want applying", data["status"])
	}
}

// A method the registry refuses outright is not a write awaiting approval.
// Calling it one sends the caller to the planning tool, which refuses it again
// for a reason they were never told the first time.
func TestRefusedMethodIsExplainedRatherThanCalledAWrite(t *testing.T) {
	h := newHarness(t, false, config.ScopeConfiguration)

	for _, tc := range []struct {
		method string
		wants  string
	}{
		// Denied outright: a .get whose output carries credentials.
		{"usermacro.get", "macro values"},
		// Denied outright: creating a script is a step away from running one.
		{"script.create", "command definition"},
		// Not in the registry at all.
		{"nonsense.frobnicate", "not in the risk registry"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			res := h.call(t, "zabbix_api_call", map[string]any{"method": tc.method})
			if !res.IsError {
				t.Fatal("a refused method was accepted")
			}
			body := envelope(t, res)["error"].(map[string]any)
			msg, _ := body["message"].(string)
			if strings.Contains(msg, "is a write") {
				t.Errorf("refusal reported as a write: %q", msg)
			}
			if !strings.Contains(msg, tc.wants) {
				t.Errorf("message = %q, want it to explain %q", msg, tc.wants)
			}
		})
	}
}
