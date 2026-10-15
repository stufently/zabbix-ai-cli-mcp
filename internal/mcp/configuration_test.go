package mcp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/ops"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
)

func mediaFixture(h *harness, kind, status string) {
	h.server.Reply("mediatype.get", []any{map[string]any{"mediatypeid": "1", "name": "Email", "type": kind, "status": status, "users": []any{map[string]any{"userid": "2", "username": "operator"}}}})
	h.server.Reply("action.get", []any{map[string]any{"actionid": "3", "name": "Notify ops"}})
	h.server.Reply("mediatype.update", map[string]any{"mediatypeids": []string{"1"}})
}

func lostFixture(t *testing.T, h *harness, templated bool) {
	t.Helper()
	rule := map[string]any{"itemid": "234350", "hostid": "10", "name": "Service Discovery", "key_": "systemd.unit.discovery[service]", "templateid": "0", "lifetime_type": "1", "lifetime": "7d", "enabled_lifetime_type": "1", "enabled_lifetime": "0"}
	h.server.Handle("discoveryrule.get", func(p map[string]any) (any, error) {
		if _, ok := p["hostids"]; ok {
			return []any{map[string]any{"itemid": "51", "hostid": "21"}, map[string]any{"itemid": "52", "hostid": "22"}}, nil
		}
		return []any{rule}, nil
	})
	h.server.Handle("template.get", func(p map[string]any) (any, error) {
		if !templated {
			return []any{}, nil
		}
		if parent, ok := p["parentTemplateids"]; ok {
			if fmt.Sprint(parent) == "[10]" {
				return []any{map[string]any{"templateid": "11", "name": "Nested"}}, nil
			}
			return []any{}, nil
		}
		return []any{map[string]any{"templateid": "10", "name": "SystemD service monitoring template"}}, nil
	})
	h.server.Reply("host.get", []any{map[string]any{"hostid": "21", "name": "agent1"}, map[string]any{"hostid": "22", "name": "agent2"}})
	h.server.Handle("item.get", func(p map[string]any) (any, error) {
		if _, ok := p["selectDiscoveryRule"]; !ok {
			t.Error("item query must select rule ownership")
		}
		if _, ok := p["selectItemDiscovery"]; !ok {
			t.Error("item query must select discovery lifecycle")
		}
		id := "234350"
		if templated {
			id = "51"
		}
		return []any{
			map[string]any{"itemid": "61", "discoveryRule": map[string]any{"itemid": id}, "itemDiscovery": map[string]any{"status": "1", "ts_disable": "0", "ts_delete": "0"}},
			map[string]any{"itemid": "62", "discoveryRule": map[string]any{"itemid": id}, "itemDiscovery": map[string]any{"status": "0", "ts_disable": "123", "ts_delete": "0"}},
			map[string]any{"itemid": "63", "discoveryRule": map[string]any{"itemid": id}, "itemDiscovery": map[string]any{"status": "0", "ts_disable": "0", "ts_delete": "0"}},
			map[string]any{"itemid": "64", "discoveryRule": map[string]any{"itemid": "999"}, "itemDiscovery": map[string]any{"status": "1", "ts_disable": "0", "ts_delete": "0"}},
		}, nil
	})
	h.server.Reply("discoveryrule.update", map[string]any{"itemids": []string{"234350"}})
}

func TestMediaTypePlanShowsDependenciesAndState(t *testing.T) {
	h := newWritableHarness(t)
	mediaFixture(h, "1", "0")
	r := h.call(t, "zabbix_plan_create", map[string]any{"operation": "mediatypes.disable", "params": map[string]any{"name": "Email"}})
	if r.IsError {
		t.Fatal(text(t, r))
	}
	for _, s := range []string{"Email", "Script", "enabled", "disabled", "operator", "Notify ops", "stop"} {
		if !strings.Contains(text(t, r), s) {
			t.Errorf("missing %q: %s", s, text(t, r))
		}
	}
	data := envelope(t, r)["data"].(map[string]any)
	if !strings.Contains(data["approve_command"].(string), "approve ") {
		t.Fatal(data)
	}
	if len(h.server.CallsTo("mediatype.update")) != 0 {
		t.Fatal("planning wrote")
	}
	calls := h.server.CallsTo("mediatype.get")
	if _, ok := calls[0].Params["selectUsers"]; !ok {
		t.Fatal("missing selectUsers")
	}
	if fmt.Sprint(h.server.CallsTo("action.get")[0].Params["mediatypeids"]) != "[1 0]" {
		t.Fatal("action scope")
	}
}

func TestMediaStateWriteIsIdempotentAndAudited(t *testing.T) {
	for _, kind := range []string{"0", "1", "4"} {
		for _, status := range []string{"0", "1"} {
			t.Run(kind+status, func(t *testing.T) {
				h := newWritableHarness(t)
				mediaFixture(h, kind, status)
				for i := 0; i < 2; i++ {
					r := h.call(t, "zabbix_write", map[string]any{"operation": "mediatypes.enable", "params": map[string]any{"mediatypeid": "1"}})
					if r.IsError {
						t.Fatal(text(t, r))
					}
					id := envelope(t, r)["data"].(map[string]any)["plan_id"].(string)
					e, err := h.audit.Find(id)
					if err != nil || e == nil || e.Operation != "mediatypes.enable" || e.Outcome != "applied" {
						t.Fatalf("audit = %+v, %v", e, err)
					}
				}
				for _, c := range h.server.CallsTo("mediatype.update") {
					if len(c.Params) != 2 || c.Params["status"] != "0" || c.Params["mediatypeid"] != "1" {
						t.Fatal(c.Params)
					}
				}
			})
		}
	}
}

func TestLostResourcesPlanAndWrite(t *testing.T) {
	for _, templated := range []bool{false, true} {
		for _, del := range []bool{false, true} {
			t.Run(fmt.Sprint(templated, del), func(t *testing.T) {
				h := newWritableHarness(t)
				lostFixture(t, h, templated)
				params := map[string]any{"itemid": "234350", "enabled_lifetime_type": "2"}
				if templated {
					delete(params, "itemid")
					params["template"] = "SystemD service monitoring template"
					params["name"] = "Service Discovery"
				}
				if del {
					params["lifetime_type"] = "2"
				}
				r := h.call(t, "zabbix_plan_create", map[string]any{"operation": "discoveryrules.set_lost_resources", "params": params})
				if r.IsError {
					t.Fatal(text(t, r))
				}
				txt := text(t, r)
				for _, s := range []string{"Service Discovery", "lost items: 2", "disable immediately"} {
					if !strings.Contains(txt, s) {
						t.Errorf("missing %q: %s", s, txt)
					}
				}
				data := envelope(t, r)["data"].(map[string]any)
				want := float64(1)
				if templated {
					want = 2
					if !strings.Contains(txt, "template") {
						t.Fatal(txt)
					}
				}
				if data["impact_count"] != want {
					t.Fatalf("impact = %v", data)
				}
				if del && (data["risk"] != "destructive" || !strings.Contains(txt, "history")) {
					t.Fatal(txt)
				}
				r = h.call(t, "zabbix_write", map[string]any{"operation": "discoveryrules.set_lost_resources", "params": params})
				if r.IsError {
					t.Fatal(text(t, r))
				}
				id := envelope(t, r)["data"].(map[string]any)["plan_id"].(string)
				e, err := h.audit.Find(id)
				if err != nil || e == nil || e.Operation != "discoveryrules.set_lost_resources" {
					t.Fatalf("audit=%+v %v", e, err)
				}
				calls := h.server.CallsTo("discoveryrule.update")
				if len(calls) != 1 || calls[0].Params["enabled_lifetime_type"] != "2" {
					t.Fatal(calls)
				}
			})
		}
	}
}

func TestLimitedRawWritesUseDomainPlans(t *testing.T) {
	for _, method := range []string{"mediatype.update", "discoveryrule.update"} {
		h := newWritableHarness(t)
		mediaFixture(h, "1", "0")
		lostFixture(t, h, true)
		p := map[string]any{"mediatypeid": "1", "status": "1"}
		if method == "discoveryrule.update" {
			p = map[string]any{"itemid": "234350", "enabled_lifetime_type": "2"}
		}
		for _, tool := range []string{"zabbix_plan_create", "zabbix_write"} {
			r := h.call(t, tool, map[string]any{"operation": "api.call", "params": map[string]any{"method": method, "params": p}})
			if r.IsError {
				t.Fatal(text(t, r))
			}
			if tool == "zabbix_plan_create" && !strings.Contains(text(t, r), map[string]string{"mediatype.update": "Notify ops", "discoveryrule.update": "lost items: 2"}[method]) {
				t.Fatal(text(t, r))
			}
		}
	}
}

func TestUnsafeFieldsNeverReachZabbix(t *testing.T) {
	h := newWritableHarness(t)
	for _, tool := range []string{"zabbix_plan_create", "zabbix_write"} {
		for _, tc := range []struct {
			op    string
			p     map[string]any
			field string
		}{
			{"mediatypes.enable", map[string]any{"mediatypeid": "1", "exec_path": "evil"}, "exec_path"},
			{"discoveryrules.set_lost_resources", map[string]any{"itemid": "1", "enabled_lifetime_type": "2", "key_": "evil"}, "key_"},
			{"api.call", map[string]any{"method": "mediatype.update", "params": map[string]any{"mediatypeid": "1", "status": "0", "exec_path": "evil"}}, "exec_path"},
			{"api.call", map[string]any{"method": "discoveryrule.update", "params": map[string]any{"itemid": "1", "enabled_lifetime_type": "2", "delay": "1s"}}, "delay"},
		} {
			r := h.call(t, tool, map[string]any{"operation": tc.op, "params": tc.p})
			if !r.IsError || !strings.Contains(text(t, r), tc.field) {
				t.Fatal(text(t, r))
			}
		}
	}
	if len(h.server.Calls()) != 0 {
		t.Fatal(h.server.Calls())
	}
}

func TestConstrainedStoredPlanCannotSmuggleFields(t *testing.T) {
	h := newWritableHarness(t)
	mediaFixture(h, "1", "1")
	lostFixture(t, h, false)
	env := &opspec.Env{Service: service.New(api.New(h.server.URL, testToken)), Profile: "test", Config: config.Profile{}, AllowWrite: true}
	for _, tc := range []struct {
		op    string
		p     map[string]any
		field string
	}{
		{"mediatypes.enable", map[string]any{"mediatypeid": "1"}, "exec_path"},
		{"discoveryrules.set_lost_resources", map[string]any{"itemid": "234350", "enabled_lifetime_type": "2"}, "key_"},
	} {
		op, ok := ops.Lookup(tc.op)
		if !ok {
			t.Fatalf("missing %s", tc.op)
		}
		args, err := op.Bind(tc.p)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := ops.CreatePlan(context.Background(), env, op, args)
		if err != nil {
			t.Fatal(err)
		}
		plan.Params[tc.field] = "evil"
		if err = plan.Seal(); err != nil {
			t.Fatal(err)
		}
		_, err = ops.Apply(context.Background(), env, plan, ops.ApplyOptions{Mode: ops.ApplyDirect, Approval: safety.ApprovalMCPWrite})
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Fatalf("smuggled %s: %v", tc.field, err)
		}
	}
	if len(h.server.CallsTo("mediatype.update"))+len(h.server.CallsTo("discoveryrule.update")) != 0 {
		t.Fatal("unsafe write")
	}
}

func TestLostUpdateSendsOnlyRequestedFields(t *testing.T) {
	for _, raw := range []bool{false, true} {
		h := newWritableHarness(t)
		lostFixture(t, h, false)
		// Existing immediate deletion must not turn a disable-only policy edit
		// into a write to the deletion policy.
		h.server.Reply("discoveryrule.get", []any{map[string]any{"itemid": "234350", "hostid": "10", "name": "Service Discovery", "key_": "systemd.unit.discovery[service]", "templateid": "0", "lifetime_type": "2", "lifetime": "0", "enabled_lifetime_type": "1", "enabled_lifetime": "0"}})
		op := "discoveryrules.set_lost_resources"
		p := map[string]any{"itemid": "234350", "enabled_lifetime_type": "2"}
		if raw {
			op = "api.call"
			p = map[string]any{"method": "discoveryrule.update", "params": p}
		}
		r := h.call(t, "zabbix_write", map[string]any{"operation": op, "params": p})
		if r.IsError {
			t.Fatal(text(t, r))
		}
		calls := h.server.CallsTo("discoveryrule.update")
		if len(calls) != 1 || len(calls[0].Params) != 2 {
			t.Fatalf("extra fields written: %+v", calls)
		}
	}
}

func TestLimitedUpdatesRejectAmbiguousSelectorsAndEmptyChange(t *testing.T) {
	for _, tc := range []struct {
		op string
		p  map[string]any
	}{
		{"mediatypes.enable", map[string]any{}},
		{"mediatypes.enable", map[string]any{"mediatypeid": "1", "name": "Email"}},
		{"discoveryrules.set_lost_resources", map[string]any{"itemid": "234350"}},
		{"discoveryrules.set_lost_resources", map[string]any{"template": "Template", "enabled_lifetime_type": "2"}},
		{"discoveryrules.set_lost_resources", map[string]any{"itemid": "234350", "template": "Template", "name": "Discovery", "enabled_lifetime_type": "2"}},
	} {
		h := newWritableHarness(t)
		r := h.call(t, "zabbix_plan_create", map[string]any{"operation": tc.op, "params": tc.p})
		if !r.IsError {
			t.Fatal(text(t, r))
		}
		if len(h.server.Calls()) != 0 {
			t.Fatal(h.server.Calls())
		}
	}
}

func TestLimitedPlansRejectDuplicateNamesAndReadFailures(t *testing.T) {
	for _, failure := range []string{"duplicate", "action", "item", "host"} {
		h := newWritableHarness(t)
		mediaFixture(h, "1", "0")
		lostFixture(t, h, true)
		op := "mediatypes.disable"
		p := map[string]any{"name": "Email"}
		switch failure {
		case "duplicate":
			h.server.Reply("mediatype.get", []any{map[string]any{"name": "Email"}, map[string]any{"name": "Email"}})
		case "action":
			h.server.Fail("action.get", -32602, "Denied", "no action access")
		case "item", "host":
			op = "discoveryrules.set_lost_resources"
			p = map[string]any{"itemid": "234350", "enabled_lifetime_type": "2"}
			h.server.Fail(failure+".get", -32602, "Denied", "cannot read impact")
		}
		r := h.call(t, "zabbix_write", map[string]any{"operation": op, "params": p})
		if !r.IsError {
			t.Fatal(text(t, r))
		}
		if len(h.server.CallsTo("mediatype.update"))+len(h.server.CallsTo("discoveryrule.update")) != 0 {
			t.Fatal("failed planning wrote")
		}
	}
}

func TestNewOperationsAreDiscoverableAndScoped(t *testing.T) {
	h := newWritableHarness(t, config.ScopeMaintenance)
	tools, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "zabbix_write" && tool.Name != "zabbix_plan_create" {
			continue
		}
		for _, name := range []string{"mediatypes.enable", "mediatypes.disable", "discoveryrules.set_lost_resources"} {
			if !strings.Contains(tool.Description, name) {
				t.Errorf("%s missing %s", tool.Name, name)
			}
		}
	}
	for _, op := range []string{"mediatypes.enable", "mediatypes.disable", "discoveryrules.set_lost_resources"} {
		p := map[string]any{"mediatypeid": "1"}
		if strings.HasPrefix(op, "discoveryrules") {
			p = map[string]any{"itemid": "234350", "enabled_lifetime_type": "2"}
		}
		for _, tool := range []string{"zabbix_write", "zabbix_plan_create"} {
			r := h.call(t, tool, map[string]any{"operation": op, "params": p})
			if !r.IsError || !strings.Contains(text(t, r), "SCOPE") {
				t.Fatal(text(t, r))
			}
		}
	}
	if len(h.server.Calls()) != 0 {
		t.Fatal("scope check reached API")
	}
}

func TestLostResourcesAcceptsNumericPolicyModes(t *testing.T) {
	h := newWritableHarness(t)
	lostFixture(t, h, false)
	r := h.call(t, "zabbix_write", map[string]any{"operation": "discoveryrules.set_lost_resources", "params": map[string]any{"itemid": "234350", "enabled_lifetime_type": 2}})
	if r.IsError {
		t.Fatal(text(t, r))
	}
}

func TestAPIReadAcceptsObjectParamsThroughSchema(t *testing.T) {
	h := newHarness(t, true)
	h.server.Reply("host.get", []any{map[string]any{"hostid": "1"}})
	r := h.call(t, "zabbix_api_call", map[string]any{"method": "host.get", "params": map[string]any{"output": []string{"hostid"}}})
	if r.IsError {
		t.Fatal(text(t, r))
	}
	if len(h.server.CallsTo("host.get")) != 1 {
		t.Fatal(h.server.Calls())
	}
}

func TestNewStoredPlansApplyWithAuditAndRejectStaleState(t *testing.T) {
	for _, opName := range []string{"mediatypes.enable", "mediatypes.disable", "discoveryrules.set_lost_resources"} {
		for _, stale := range []bool{false, true} {
			t.Run(fmt.Sprint(opName, stale), func(t *testing.T) {
				h := newWritableHarness(t)
				mediaFixture(h, "1", "0")
				lostFixture(t, h, false)
				p := map[string]any{"mediatypeid": "1"}
				if strings.HasPrefix(opName, "discoveryrules") {
					p = map[string]any{"itemid": "234350", "lifetime_type": "2"}
				}
				r := h.call(t, "zabbix_plan_create", map[string]any{"operation": opName, "params": p})
				if r.IsError {
					t.Fatal(text(t, r))
				}
				id := envelope(t, r)["data"].(map[string]any)["plan_id"].(string)
				plan, err := h.plans.Load(id)
				if err != nil {
					t.Fatal(err)
				}
				method := "mediatype.update"
				if stale {
					if strings.HasPrefix(opName, "discoveryrules") {
						h.server.Reply("discoveryrule.get", []any{})
					} else {
						mediaFixture(h, "1", "1")
					}
				}
				if strings.HasPrefix(opName, "discoveryrules") {
					method = "discoveryrule.update"
				}
				env := &opspec.Env{Service: service.New(api.New(h.server.URL, testToken)), Profile: "test", Config: config.Profile{Scopes: []string{config.ScopeConfiguration}}, Plans: h.plans, Audit: h.audit}
				_, err = ops.Apply(context.Background(), env, plan, ops.ApplyOptions{Mode: ops.ApplyStored, Confirm: plan.RequiresConfirmName, Approval: safety.ApprovalTerminal})
				if stale {
					if err == nil || len(h.server.CallsTo(method)) != 0 {
						t.Fatalf("stale plan wrote: %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					entry, err := h.audit.Find(id)
					if err != nil || entry == nil || entry.Approval != safety.ApprovalTerminal || entry.Outcome != "applied" {
						t.Fatalf("audit=%+v %v", entry, err)
					}
				}
			})
		}
	}
}

func TestLostResourceRiskCannotBeDowngraded(t *testing.T) {
	h := newWritableHarness(t)
	lostFixture(t, h, false)
	env := &opspec.Env{Service: service.New(api.New(h.server.URL, testToken)), Profile: "test", AllowWrite: true}
	for _, raw := range []bool{false, true} {
		name := "discoveryrules.set_lost_resources"
		p := map[string]any{"itemid": "234350", "lifetime_type": "2"}
		if raw {
			name = "api.call"
			p = map[string]any{"method": "discoveryrule.update", "params": p}
		}
		op, _ := ops.Lookup(name)
		args, err := op.Bind(p)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := ops.CreatePlan(context.Background(), env, op, args)
		if err != nil {
			t.Fatal(err)
		}
		plan.Risk = safety.RiskWrite
		plan.RequiresConfirmName = ""
		if err = plan.Seal(); err != nil {
			t.Fatal(err)
		}
		_, err = ops.Apply(context.Background(), env, plan, ops.ApplyOptions{Mode: ops.ApplyDirect, Approval: safety.ApprovalMCPWrite})
		if err == nil {
			t.Fatal("downgraded deletion applied")
		}
	}
	if len(h.server.CallsTo("discoveryrule.update")) != 0 {
		t.Fatal("unsafe write")
	}
}

func TestRawMediaStatusNormalizationMatchesPlannedEffect(t *testing.T) {
	for _, status := range []any{0, "0", "00", " 0 "} {
		h := newWritableHarness(t)
		mediaFixture(h, "1", "1")
		r := h.call(t, "zabbix_plan_create", map[string]any{"operation": "api.call", "params": map[string]any{"method": "mediatype.update", "params": map[string]any{"mediatypeid": "1", "status": status}}})
		if r.IsError || !strings.Contains(text(t, r), "Enable media type") {
			t.Fatalf("status=%v: %s", status, text(t, r))
		}
	}
}

func TestTemplateImpactTraversesNestedLinks(t *testing.T) {
	h := newWritableHarness(t)
	lostFixture(t, h, true)
	r := h.call(t, "zabbix_plan_create", map[string]any{"operation": "discoveryrules.set_lost_resources", "params": map[string]any{"itemid": "234350", "enabled_lifetime_type": 2}})
	if r.IsError {
		t.Fatal(text(t, r))
	}
	nestedRead := false
	for _, c := range h.server.CallsTo("template.get") {
		if fmt.Sprint(c.Params["parentTemplateids"]) == "[11]" {
			nestedRead = true
		}
	}
	if !nestedRead {
		t.Fatal("nested template links were not read")
	}
	calls := h.server.CallsTo("host.get")
	if len(calls) != 1 || fmt.Sprint(calls[0].Params["templateids"]) != "[10 11]" {
		t.Fatalf("host scope=%+v", calls)
	}
	for _, c := range h.server.CallsTo("item.get") {
		if fmt.Sprint(c.Params["hostids"]) != "[21 22]" {
			t.Fatal(c.Params)
		}
	}
}

func TestRawLostPolicyModeNormalization(t *testing.T) {
	h := newWritableHarness(t)
	lostFixture(t, h, false)
	r := h.call(t, "zabbix_plan_create", map[string]any{"operation": "api.call", "params": map[string]any{"method": "discoveryrule.update", "params": map[string]any{"itemid": "234350", "lifetime_type": "02", "enabled_lifetime_type": " 2 "}}})
	if r.IsError {
		t.Fatal(text(t, r))
	}
	for _, want := range []string{"delete immediately", "disable immediately", "destructive"} {
		if !strings.Contains(text(t, r), want) {
			t.Fatalf("missing %s: %s", want, text(t, r))
		}
	}
}

func TestConstrainedDestructivePlanCannotDropConfirmation(t *testing.T) {
	for _, opName := range []string{"mediatypes.disable", "discoveryrules.set_lost_resources", "api.call"} {
		h := newWritableHarness(t)
		mediaFixture(h, "1", "0")
		lostFixture(t, h, false)
		env := &opspec.Env{Service: service.New(api.New(h.server.URL, testToken)), Profile: "test", AllowWrite: true}
		p := map[string]any{"mediatypeid": "1"}
		if opName == "discoveryrules.set_lost_resources" {
			p = map[string]any{"itemid": "234350", "lifetime_type": 2}
		}
		if opName == "api.call" {
			p = map[string]any{"method": "discoveryrule.update", "params": map[string]any{"itemid": "234350", "lifetime_type": 2}}
		}
		op, _ := ops.Lookup(opName)
		args, err := op.Bind(p)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := ops.CreatePlan(context.Background(), env, op, args)
		if err != nil {
			t.Fatal(err)
		}
		plan.RequiresConfirmName = ""
		if err = plan.Seal(); err != nil {
			t.Fatal(err)
		}
		_, err = ops.Apply(context.Background(), env, plan, ops.ApplyOptions{Mode: ops.ApplyStored, Approval: safety.ApprovalTerminal})
		if err == nil {
			t.Fatalf("%s applied without required target", opName)
		}
		if len(h.server.CallsTo("mediatype.update"))+len(h.server.CallsTo("discoveryrule.update")) != 0 {
			t.Fatal("unsafe write")
		}
	}
}

func TestLLDPlanRejectsSameSizeTopologyReplacement(t *testing.T) {
	for _, replacement := range []string{"host", "template"} {
		h := newWritableHarness(t)
		lostFixture(t, h, true)
		env := &opspec.Env{Service: service.New(api.New(h.server.URL, testToken)), Profile: "test", AllowWrite: true}
		op, _ := ops.Lookup("discoveryrules.set_lost_resources")
		args, err := op.Bind(map[string]any{"itemid": "234350", "lifetime_type": 2})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := ops.CreatePlan(context.Background(), env, op, args)
		if err != nil {
			t.Fatal(err)
		}
		if replacement == "host" {
			h.server.Reply("host.get", []any{map[string]any{"hostid": "21"}, map[string]any{"hostid": "99"}})
		} else {
			h.server.Handle("template.get", func(p map[string]any) (any, error) {
				if fmt.Sprint(p["parentTemplateids"]) == "[10]" {
					return []any{map[string]any{"templateid": "99"}}, nil
				}
				return []any{}, nil
			})
		}
		_, err = ops.Apply(context.Background(), env, plan, ops.ApplyOptions{Mode: ops.ApplyStored, Confirm: plan.RequiresConfirmName, Approval: safety.ApprovalTerminal})
		if err == nil || len(h.server.CallsTo("discoveryrule.update")) != 0 {
			t.Fatalf("replacement %s applied: %v", replacement, err)
		}
	}
}
