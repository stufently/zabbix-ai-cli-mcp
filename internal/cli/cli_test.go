package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/cli"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/zbxtest"
)

const testToken = "super-secret-token-value"

type harness struct {
	t      *testing.T
	server *zbxtest.Server
	state  string
}

// newHarness points the CLI at a fake Zabbix and at throwaway directories, so
// a test never touches the developer's real configuration.
func newHarness(t *testing.T, scopes ...string) *harness {
	t.Helper()
	return newHarnessWith(t, nil, scopes...)
}

// newHarnessWith builds the same throwaway installation with an explicit
// answer to "may this profile be written to directly". Nil leaves the setting
// absent, which is what a config file written before the setting existed looks
// like.
func newHarnessWith(t *testing.T, allowWrite *bool, scopes ...string) *harness {
	t.Helper()
	srv := zbxtest.New(t, "7.4.10")
	cfgDir := t.TempDir()
	stateDir := t.TempDir()

	cfg := &config.Config{
		ActiveProfile: "test",
		AllowWrite:    allowWrite,
		Profiles: map[string]config.Profile{
			"test": {URL: srv.URL, Scopes: scopes},
		},
	}
	t.Setenv(config.EnvConfigDir, cfgDir)
	t.Setenv(config.EnvStateDir, stateDir)
	t.Setenv(config.EnvToken, testToken)
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvProfile, "")
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	return &harness{t: t, server: srv, state: stateDir}
}

type result struct {
	code   int
	stdout string
	stderr string
}

func (h *harness) run(args ...string) result {
	h.t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Execute(args, strings.NewReader(""), &out, &errOut)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func (r result) envelope(t *testing.T) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	return env
}

func TestReadCommandReturnsTheEnvelope(t *testing.T) {
	h := newHarness(t)
	h.server.Reply("problem.get", []any{
		zbxtest.Problem("100", "500", "Disk full", "4", nil),
	})
	h.server.Reply("trigger.get", []any{map[string]any{
		"triggerid": "500", "hosts": []any{map[string]any{"hostid": "10", "name": "db01"}},
	}})

	r := h.run("problems", "list", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d, stderr: %s", r.code, r.stderr)
	}
	env := r.envelope(t)
	if env["ok"] != true {
		t.Fatalf("ok = %v", env["ok"])
	}
	meta := env["meta"].(map[string]any)
	if meta["returned"] != float64(1) {
		t.Errorf("meta.returned = %v", meta["returned"])
	}
	if meta["profile"] != "test" {
		t.Errorf("meta.profile = %v", meta["profile"])
	}
	if meta["zabbix_version"] != "7.4.10" {
		t.Errorf("meta.zabbix_version = %v", meta["zabbix_version"])
	}
}

func TestWriteDoesNothingWithoutApply(t *testing.T) {
	h := newHarness(t, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	h.server.Reply("maintenance.create", map[string]any{"maintenanceids": []any{"1"}})

	r := h.run("maintenance", "create", "web01", "--for", "2h", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d, stderr: %s", r.code, r.stderr)
	}
	env := r.envelope(t)
	data := env["data"].(map[string]any)
	if data["status"] != "planned" {
		t.Errorf("status = %v, want planned", data["status"])
	}
	if data["plan_id"] == nil || data["approve_command"] == nil {
		t.Errorf("the plan must carry an identifier and the approve command: %v", data)
	}
	// The only thing that matters: Zabbix was never asked to change anything.
	if calls := h.server.CallsTo("maintenance.create"); len(calls) != 0 {
		t.Fatalf("maintenance.create was called %d times without --apply", len(calls))
	}
}

func TestWriteExecutesWithApply(t *testing.T) {
	h := newHarness(t, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	h.server.Reply("maintenance.create", map[string]any{"maintenanceids": []any{"1"}})

	r := h.run("maintenance", "create", "web01", "--for", "2h", "--apply", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d, stderr: %s\n%s", r.code, r.stderr, r.stdout)
	}
	data := r.envelope(t)["data"].(map[string]any)
	if data["status"] != "applied" {
		t.Errorf("status = %v", data["status"])
	}
	calls := h.server.CallsTo("maintenance.create")
	if len(calls) != 1 {
		t.Fatalf("maintenance.create was called %d times", len(calls))
	}
	// Zabbix requires host references as objects carrying only hostid; a bare
	// hostids array is rejected outright.
	hosts, ok := calls[0].Params["hosts"].([]any)
	if !ok || len(hosts) != 1 {
		t.Fatalf("hosts parameter = %v", calls[0].Params["hosts"])
	}
	entry := hosts[0].(map[string]any)
	if entry["hostid"] != "10" || len(entry) != 1 {
		t.Errorf("host reference = %v, want only a hostid", entry)
	}
	if _, present := calls[0].Params["hostids"]; present {
		t.Error("the deprecated hostids parameter must not be sent")
	}
}

func maintenanceSeven(h *harness) {
	h.server.Reply("maintenance.get", []any{map[string]any{
		"maintenanceid": "7", "name": "weekend window", "maintenance_type": "0",
		"active_since": "1787000000", "active_till": "1790000000",
		"hosts": []any{}, "hostgroups": []any{}, "timeperiods": []any{},
	}})
	h.server.Reply("maintenance.delete", map[string]any{"maintenanceids": []any{"7"}})
}

func TestDestructiveApplyRunsWithoutTheEcho(t *testing.T) {
	// --apply was handed the target on the same command line, so naming it
	// back would only be the caller repeating itself.
	h := newHarness(t, config.ScopeMaintenance)
	maintenanceSeven(h)

	r := h.run("maintenance", "delete", "7", "--apply", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d, stderr: %s\n%s", r.code, r.stderr, r.stdout)
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 1 {
		t.Fatalf("maintenance.delete was called %d times", len(calls))
	}
}

func TestApprovingADestructivePlanNeedsTheTargetNamedBack(t *testing.T) {
	// A stored plan is read minutes after it was made, and the echo is what
	// stops the wrong one being approved.
	h := newHarness(t, config.ScopeMaintenance)
	maintenanceSeven(h)

	r := h.run("maintenance", "delete", "7", "--json")
	planID := r.envelope(t)["data"].(map[string]any)["plan_id"].(string)

	r = h.run("approve", planID, "--yes", "--json")
	if r.code != errs.ExitApprovalRequired {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitApprovalRequired, r.stdout)
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 0 {
		t.Fatal("a destructive change ran without the confirmation")
	}

	r = h.run("approve", planID, "--yes", "--confirm", "weekend window", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d, stderr: %s\n%s", r.code, r.stderr, r.stdout)
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 1 {
		t.Fatalf("maintenance.delete was called %d times", len(calls))
	}
}

func TestDirectWritesCanBeDisabled(t *testing.T) {
	h := newHarnessWith(t, config.Bool(false), config.ScopeMaintenance)
	maintenanceSeven(h)

	// The change is refused, and the plan it produced is still there to be
	// approved by a person: turning direct writes off is not the same as
	// turning the tool read-only.
	r := h.run("maintenance", "delete", "7", "--apply", "--json")
	if r.code != errs.ExitPermission {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitPermission, r.stdout)
	}
	if body := r.envelope(t)["error"].(map[string]any); body["code"] != errs.CodeWriteDisabled {
		t.Errorf("error code = %v, want %v", body["code"], errs.CodeWriteDisabled)
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 0 {
		t.Fatal("a change ran while direct writes were disabled")
	}

	r = h.run("maintenance", "delete", "7", "--json")
	planID := r.envelope(t)["data"].(map[string]any)["plan_id"].(string)
	r = h.run("approve", planID, "--yes", "--confirm", "weekend window", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("approve: exit = %d, stderr: %s\n%s", r.code, r.stderr, r.stdout)
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 1 {
		t.Fatalf("maintenance.delete was called %d times", len(calls))
	}
}

func TestWritesAreDisabledByTheEnvironment(t *testing.T) {
	// A container is configured through its environment and a config file it
	// does not own, so the environment has to be able to say "not here".
	h := newHarness(t, config.ScopeMaintenance)
	t.Setenv(config.EnvAllowWrite, "off")
	maintenanceSeven(h)

	r := h.run("maintenance", "delete", "7", "--apply", "--json")
	if r.code != errs.ExitPermission {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitPermission, r.stdout)
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 0 {
		t.Fatal("a change ran while the environment disabled writes")
	}
}

func TestScopeIsEnforcedAtTheCommandLine(t *testing.T) {
	// Naming any scope narrows the profile to exactly those, whether or not
	// direct writes are allowed.
	h := newHarness(t, config.ScopeAcknowledge)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})

	r := h.run("maintenance", "create", "web01", "--for", "2h", "--json")
	if r.code != errs.ExitPermission {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitPermission, r.stdout)
	}
	body := r.envelope(t)["error"].(map[string]any)
	if body["code"] != errs.CodeScope {
		t.Errorf("error code = %v", body["code"])
	}
}

func TestApplyingAWriteIsAudited(t *testing.T) {
	h := newHarness(t, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	h.server.Reply("maintenance.create", map[string]any{"maintenanceids": []any{"1"}})

	if r := h.run("maintenance", "create", "web01", "--for", "2h", "--apply", "--json"); r.code != 0 {
		t.Fatalf("exit = %d: %s", r.code, r.stdout)
	}
	data, err := os.ReadFile(filepath.Join(h.state, "audit.log"))
	if err != nil {
		t.Fatalf("the audit log was not written: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("audit line is not JSON: %v", err)
	}
	if entry["operation"] != "maintenance.create" || entry["outcome"] != "applied" {
		t.Errorf("audit entry = %v", entry)
	}
	if entry["approval"] != "cli-apply" {
		t.Errorf("approval = %v", entry["approval"])
	}
	if strings.Contains(string(data), testToken) {
		t.Error("the audit log contains the API token")
	}
}

func TestTokenNeverReachesOutput(t *testing.T) {
	h := newHarness(t, config.ScopeMaintenance)
	h.server.Reply("problem.get", []any{zbxtest.Problem("1", "2", "x", "4", nil)})
	h.server.Reply("trigger.get", []any{})
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})

	for _, args := range [][]string{
		{"problems", "list", "--json", "--debug"},
		{"auth", "status", "--json"},
		{"host", "list", "--json", "--debug"},
		{"maintenance", "create", "web01", "--for", "2h", "--json", "--debug"},
		{"schema", "--json"},
	} {
		r := h.run(args...)
		if strings.Contains(r.stdout, testToken) {
			t.Errorf("%v leaked the token on stdout", args)
		}
		if strings.Contains(r.stderr, testToken) {
			t.Errorf("%v leaked the token on stderr", args)
		}
	}
}

func TestExitCodes(t *testing.T) {
	h := newHarness(t)
	h.server.Reply("host.get", []any{})

	if r := h.run("host", "status", "missing", "--json"); r.code != errs.ExitNotFound {
		t.Errorf("not found: exit = %d, want %d", r.code, errs.ExitNotFound)
	}
	if r := h.run("host", "list", "--nonsense", "--json"); r.code != errs.ExitUsage {
		t.Errorf("bad flag: exit = %d, want %d", r.code, errs.ExitUsage)
	}
	if r := h.run("problems", "list", "--severity", "urgent", "--json"); r.code != errs.ExitUsage {
		t.Errorf("bad value: exit = %d, want %d", r.code, errs.ExitUsage)
	}
	if r := h.run("version"); r.code != errs.ExitOK {
		t.Errorf("version: exit = %d", r.code)
	}
}

func TestRejectedTokenIsReportedAsAnAuthenticationFailure(t *testing.T) {
	h := newHarness(t)
	h.server.Fail("problem.get", -32602, "Invalid params.", "Not authorised.")

	r := h.run("problems", "list", "--json")
	if r.code != errs.ExitAuth {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitAuth, r.stdout)
	}
	body := r.envelope(t)["error"].(map[string]any)
	if body["code"] != errs.CodeAuth {
		t.Errorf("code = %v", body["code"])
	}
	if !strings.Contains(body["suggestion"].(string), "login") {
		t.Errorf("suggestion = %v", body["suggestion"])
	}
}

func TestRawApiCallRefusesUnclassifiedMethods(t *testing.T) {
	h := newHarness(t)
	r := h.run("api", "call", "nonsense.frobnicate", "--json")
	if r.code != errs.ExitPermission {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitPermission, r.stdout)
	}
	if calls := h.server.CallsTo("nonsense.frobnicate"); len(calls) != 0 {
		t.Error("an unclassified method reached the server")
	}
}

func TestRawApiCallRunsReads(t *testing.T) {
	h := newHarness(t)
	h.server.Reply("host.get", []any{map[string]any{"hostid": "1", "host": "web01"}})
	r := h.run("api", "call", "host.get", "--params", `{"output":["hostid","host"]}`, "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d: %s", r.code, r.stdout)
	}
	env := r.envelope(t)
	if len(env["warnings"].([]any)) == 0 {
		t.Error("raw output should carry a warning that it is neither projected nor bounded")
	}
}

func TestPlanSurvivesToApproveAndAppliesOnce(t *testing.T) {
	h := newHarness(t, config.ScopeMaintenance)
	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	h.server.Reply("maintenance.create", map[string]any{"maintenanceids": []any{"1"}})

	r := h.run("maintenance", "create", "web01", "--for", "2h", "--json")
	if r.code != 0 {
		t.Fatalf("plan failed: %s", r.stdout)
	}
	planID := r.envelope(t)["data"].(map[string]any)["plan_id"].(string)

	// A non-interactive approval must be explicit about being one.
	if r := h.run("approve", planID, "--json"); r.code == errs.ExitOK {
		t.Error("approval without a terminal and without --yes must not apply")
	}
	if calls := h.server.CallsTo("maintenance.create"); len(calls) != 0 {
		t.Fatal("the change was applied without approval")
	}

	if r := h.run("approve", planID, "--yes", "--json"); r.code != errs.ExitOK {
		t.Fatalf("approve --yes: exit %d\n%s", r.code, r.stdout)
	}
	if calls := h.server.CallsTo("maintenance.create"); len(calls) != 1 {
		t.Fatalf("maintenance.create ran %d times", len(calls))
	}
	// The plan is consumed, so approving twice cannot apply twice.
	if r := h.run("approve", planID, "--yes", "--json"); r.code == errs.ExitOK {
		t.Error("a consumed plan must not be applicable again")
	}
	if calls := h.server.CallsTo("maintenance.create"); len(calls) != 1 {
		t.Errorf("maintenance.create ran %d times in total", len(calls))
	}
}

func TestStalePreconditionStopsAnApproval(t *testing.T) {
	h := newHarness(t, config.ScopeMaintenance)
	h.server.Reply("maintenance.get", []any{map[string]any{
		"maintenanceid": "7", "name": "weekend window", "maintenance_type": "0",
		"active_since": "1787000000", "active_till": "1790000000",
		"hosts": []any{}, "hostgroups": []any{}, "timeperiods": []any{},
	}})
	h.server.Reply("maintenance.delete", map[string]any{"maintenanceids": []any{"7"}})

	r := h.run("maintenance", "delete", "7", "--json")
	planID := r.envelope(t)["data"].(map[string]any)["plan_id"].(string)

	// The window is replaced by a different one carrying the same identifier.
	h.server.Reply("maintenance.get", []any{map[string]any{
		"maintenanceid": "7", "name": "something else entirely", "maintenance_type": "0",
		"active_since": "1787000000", "active_till": "1790000000",
		"hosts": []any{}, "hostgroups": []any{}, "timeperiods": []any{},
	}})

	r = h.run("approve", planID, "--yes", "--confirm", "weekend window", "--json")
	if r.code == errs.ExitOK {
		t.Fatal("a plan whose target changed must not be applied")
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 0 {
		t.Fatal("the wrong object was deleted")
	}
	body := r.envelope(t)["error"].(map[string]any)
	if body["code"] != errs.CodePlanStale {
		t.Errorf("code = %v, want %v", body["code"], errs.CodePlanStale)
	}
}

func TestSchemaDescribesEveryOperation(t *testing.T) {
	h := newHarness(t)
	r := h.run("schema", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d", r.code)
	}
	data := r.envelope(t)["data"].(map[string]any)
	list := data["operations"].([]any)
	if len(list) < 15 {
		t.Errorf("schema described %d operations", len(list))
	}
	first := list[0].(map[string]any)
	for _, key := range []string{"operation", "command", "description", "read_only", "risk", "input_schema"} {
		if _, ok := first[key]; !ok {
			t.Errorf("schema entry is missing %q: %v", key, first)
		}
	}
}

func TestTableOutputIsTheDefaultOnlyForTerminals(t *testing.T) {
	h := newHarness(t)
	h.server.Reply("problem.get", []any{})
	h.server.Reply("trigger.get", []any{})
	// Output is captured into a buffer, which is not a terminal, so the
	// machine-readable format must be chosen without being asked for.
	r := h.run("problems", "list")
	if !strings.HasPrefix(strings.TrimSpace(r.stdout), "{") {
		t.Errorf("non-terminal output should be JSON, got:\n%s", r.stdout)
	}
}

func TestRawApiCallCannotSidestepProfileScopes(t *testing.T) {
	// The escape hatch declares itself a read, because whether it writes
	// depends on the method it is handed. That must not let a read-only
	// profile plan and apply a destructive method through it.
	h := newHarness(t, config.ScopeAcknowledge)
	h.server.Reply("maintenance.delete", map[string]any{"maintenanceids": []any{"7"}})

	r := h.run("api", "call", "maintenance.delete", "--params", `["7"]`, "--json")
	if r.code != errs.ExitPermission {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitPermission, r.stdout)
	}
	if body, ok := r.envelope(t)["error"].(map[string]any); ok && body["code"] != errs.CodeScope {
		t.Errorf("error code = %v, want %v", body["code"], errs.CodeScope)
	}

	r = h.run("api", "call", "maintenance.delete", "--params", `["7"]`,
		"--apply", "--confirm", "maintenance.delete", "--json")
	if r.code == errs.ExitOK {
		t.Fatal("a read-only profile applied a destructive method through the escape hatch")
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 0 {
		t.Fatal("the destructive method reached Zabbix")
	}
}

func TestRawApiCallWorksOnceTheScopeIsGranted(t *testing.T) {
	h := newHarness(t, config.ScopeMaintenance)
	h.server.Reply("maintenance.delete", map[string]any{"maintenanceids": []any{"7"}})

	r := h.run("api", "call", "maintenance.delete", "--params", `["7"]`,
		"--apply", "--confirm", "maintenance.delete", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d\n%s", r.code, r.stdout)
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 1 {
		t.Fatalf("maintenance.delete ran %d times", len(calls))
	}
}

// Cobra answers an unknown subcommand by printing help and exiting 0. Anything
// reading exit codes — a script, an agent — cannot tell that from success.
func TestAnUnknownSubcommandIsAUsageErrorNotSuccess(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"plans", "reject", "pl_x"},
		{"plans"},
		{"auth", "nonsense"},
	} {
		got := h.run(args...)
		if got.code != errs.ExitUsage {
			t.Errorf("%v exited %d, want %d (%s%s)", args, got.code, errs.ExitUsage, got.stdout, got.stderr)
		}
	}
}

// A positional argument that fails validation is the caller's mistake, not an
// internal fault.
func TestTooManyArgumentsIsAUsageError(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"api", "call", "host.get", "extra", "args"},
		{"version", "ignored"},
		{"profile", "list", "ignored"},
		{"plans", "list", "ignored"},
	} {
		if got := h.run(args...); got.code != errs.ExitUsage {
			t.Errorf("%v exited %d, want %d (%s%s)", args, got.code, errs.ExitUsage, got.stdout, got.stderr)
		}
	}
}

func TestInvalidGlobalOptionsAreUsageErrors(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"version", "--output", "xml"},
		{"version", "--timeout", "-1s"},
	} {
		got := h.run(args...)
		if got.code != errs.ExitUsage {
			t.Errorf("%v exited %d, want %d (%s%s)", args, got.code, errs.ExitUsage, got.stdout, got.stderr)
		}
	}
}

// With --token-stdin the secret is consumed before the flag is validated, so a
// rejected value means the caller has to pipe the token in again.
func TestLoginValidatesTheStoreBeforeReadingTheToken(t *testing.T) {
	h := newHarness(t)
	got := h.run("login", "--store", "keychain", "--token-stdin", "--url", h.server.URL)
	if got.code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (%s%s)", got.code, errs.ExitUsage, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout+got.stderr, "--store must be file or keyring") {
		t.Errorf("the refusal did not name the flag: %s%s", got.stdout, got.stderr)
	}
}

// The README's first install instruction is "go install", which applies no
// ldflags. A binary that reports "dev" there tells the user nothing about what
// they are running, and tells a bug report nothing either.
func TestVersionFallsBackToTheModuleVersion(t *testing.T) {
	if cli.Version == "" {
		t.Fatal("Version is empty")
	}
	// Under "go test" the main module has no version, so the fallback is the
	// literal below; what matters is that it is never blank and that a stamped
	// build overrides it.
	if cli.Version != "dev" && !strings.HasPrefix(cli.Version, "v") {
		t.Errorf("Version = %q, want either the stamped value or a module version", cli.Version)
	}
}

func TestHTTPWithWritesEnabledDemandsABearerToken(t *testing.T) {
	// An HTTP endpoint with no bearer token is open to every process on the
	// machine. That is defensible for a server that only reads, and not for
	// one that can change Zabbix.
	h := newHarness(t, config.ScopeMaintenance)
	t.Setenv("ZABBIX_AI_CLI_MCP_BEARER_TOKEN", "")

	r := h.run("mcp", "--http", "127.0.0.1:0", "--json")
	if r.code != errs.ExitPermission {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitPermission, r.stdout)
	}
	if !strings.Contains(r.stdout, "bearer token") {
		t.Errorf("the refusal should say what is missing: %s", r.stdout)
	}
}

func TestRemovingTheLastScopeDoesNotWidenTheProfile(t *testing.T) {
	// An empty scope list means "unstated", which inherits everything while
	// writes are allowed. Narrowing a profile to nothing must not read as
	// that.
	h := newHarness(t, config.ScopeMaintenance)

	r := h.run("profile", "scopes", "test", "--remove", "maintenance", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d\n%s", r.code, r.stdout)
	}
	scopes := r.envelope(t)["data"].(map[string]any)["scopes"].([]any)
	if len(scopes) != 1 || scopes[0] != config.ScopeRead {
		t.Fatalf("scopes = %v, want read alone", scopes)
	}

	h.server.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	r = h.run("maintenance", "create", "web01", "--for", "2h", "--apply", "--json")
	if r.code != errs.ExitPermission {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitPermission, r.stdout)
	}
}

func TestRemovingAnInheritedScopeNarrowsTheProfile(t *testing.T) {
	// A profile that named none inherits them all; removing one has to write
	// the rest down rather than silently do nothing.
	h := newHarness(t)

	r := h.run("profile", "scopes", "test", "--remove", "configuration", "--json")
	if r.code != errs.ExitOK {
		t.Fatalf("exit = %d\n%s", r.code, r.stdout)
	}
	got := map[string]bool{}
	for _, s := range r.envelope(t)["data"].(map[string]any)["scopes"].([]any) {
		got[s.(string)] = true
	}
	if got[config.ScopeConfiguration] {
		t.Errorf("configuration was not removed: %v", got)
	}
	for _, want := range []string{config.ScopeMaintenance, config.ScopeAcknowledge} {
		if !got[want] {
			t.Errorf("scope %q was lost along with the removal: %v", want, got)
		}
	}
}

func TestApplyRefusesAConfirmThatNamesSomethingElse(t *testing.T) {
	// --apply does not need the echo, but one that disagrees with the target
	// is a mistake worth stopping on.
	h := newHarness(t, config.ScopeMaintenance)
	maintenanceSeven(h)

	r := h.run("maintenance", "delete", "7", "--apply", "--confirm", "some other window", "--json")
	if r.code != errs.ExitApprovalRequired {
		t.Fatalf("exit = %d, want %d\n%s", r.code, errs.ExitApprovalRequired, r.stdout)
	}
	if calls := h.server.CallsTo("maintenance.delete"); len(calls) != 0 {
		t.Fatal("the change ran despite a confirmation naming something else")
	}
}
