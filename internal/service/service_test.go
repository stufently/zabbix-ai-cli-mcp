package service_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/api"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/zbxtest"
)

func newService(t *testing.T, srv *zbxtest.Server) *service.Service {
	t.Helper()
	return service.New(api.New(srv.URL, "test-token"))
}

func TestVersionIsFetchedOnceAndUnauthenticated(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	svc := newService(t, srv)

	for i := 0; i < 3; i++ {
		v, err := svc.Version(context.Background())
		if err != nil {
			t.Fatalf("Version: %v", err)
		}
		if v.String() != "7.4.10" {
			t.Fatalf("version = %q", v.String())
		}
	}
	calls := srv.CallsTo("apiinfo.version")
	if len(calls) != 1 {
		t.Errorf("apiinfo.version was called %d times; it must be cached", len(calls))
	}
	// Zabbix rejects apiinfo.version outright if an authorization header is
	// present, so the client must omit it for this one method.
	if calls[0].Auth != "" {
		t.Errorf("apiinfo.version carried an Authorization header: %q", calls[0].Auth)
	}
}

func TestOtherCallsAreAuthenticated(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{})
	svc := newService(t, srv)
	if _, _, err := svc.ListHosts(context.Background(), service.HostQuery{Limit: 5}); err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	calls := srv.CallsTo("host.get")
	if len(calls) != 1 || calls[0].Auth != "Bearer test-token" {
		t.Fatalf("host.get auth = %q", calls[0].Auth)
	}
}

func TestSearchUsesSubstringMatchingUnlessAWildcardIsGiven(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{zbxtest.Host("1", "web01", nil)})
	svc := newService(t, srv)

	// Zabbix stops applying implicit wildcards once searchWildcardsEnabled is
	// set, which silently turns a substring search into an exact match.
	if _, _, err := svc.ListHosts(context.Background(), service.HostQuery{Search: "web", Limit: 5}); err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	params := srv.CallsTo("host.get")[0].Params
	if _, present := params["searchWildcardsEnabled"]; present {
		t.Error("a plain fragment must not enable wildcard matching")
	}
	if params["searchByAny"] != true {
		t.Error("searchByAny must be set so both name fields are considered")
	}

	srv.Reset()
	if _, _, err := svc.ListHosts(context.Background(), service.HostQuery{Search: "ms*", Limit: 5}); err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	params = srv.CallsTo("host.get")[0].Params
	if params["searchWildcardsEnabled"] != true {
		t.Error("an explicit wildcard must enable wildcard matching")
	}
}

func TestListHostsReportsTruncation(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	rows := make([]any, 0, 4)
	for _, n := range []string{"a", "b", "c", "d"} {
		rows = append(rows, zbxtest.Host(n, "host-"+n, nil))
	}
	srv.Reply("host.get", rows)
	svc := newService(t, srv)

	hosts, truncated, err := svc.ListHosts(context.Background(), service.HostQuery{Limit: 3})
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if !truncated {
		t.Error("a full extra row must be reported as truncation")
	}
	if len(hosts) != 3 {
		t.Errorf("returned %d hosts, want the limit of 3", len(hosts))
	}
	// The limit sent to Zabbix asks for one more row than the caller wants, so
	// truncation is detected without a second counting query.
	if got := srv.CallsTo("host.get")[0].Params["limit"]; got != float64(4) {
		t.Errorf("limit sent to Zabbix = %v, want 4", got)
	}
}

func TestResolveHostRejectsAnAmbiguousPattern(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{
		zbxtest.Host("1", "web01-staging", nil),
		zbxtest.Host("2", "web01-backup", nil),
	})
	svc := newService(t, srv)

	_, err := svc.ResolveHost(context.Background(), "web01")
	if err == nil {
		t.Fatal("an ambiguous pattern must not silently pick a host")
	}
	if !strings.Contains(err.Error(), "web01-staging") {
		t.Errorf("the error must list the candidates: %v", err)
	}
}

func TestResolveHostPrefersAnExactMatch(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{
		zbxtest.Host("1", "web01-staging", nil),
		zbxtest.Host("2", "web01", nil),
	})
	svc := newService(t, srv)

	h, err := svc.ResolveHost(context.Background(), "web01")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if h.Name != "web01" {
		t.Errorf("resolved %q, want the exact match", h.Name)
	}
}

func TestMissingHostSuggestsASearch(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{})
	svc := newService(t, srv)

	_, err := svc.ResolveHost(context.Background(), "nope")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestProblemsResolveHostsThroughTriggers(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("problem.get", []any{
		zbxtest.Problem("100", "500", "Disk full", "4", nil),
	})
	srv.Reply("trigger.get", []any{map[string]any{
		"triggerid": "500",
		"hosts":     []any{map[string]any{"hostid": "10", "name": "db01"}},
	}})
	svc := newService(t, srv)

	problems, _, err := svc.ListProblems(context.Background(), service.ProblemQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListProblems: %v", err)
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems", len(problems))
	}
	if len(problems[0].Hosts) != 1 || problems[0].Hosts[0].Name != "db01" {
		t.Errorf("host was not resolved: %+v", problems[0].Hosts)
	}
	// problem.get has no selectHosts parameter; asking for one is an error.
	if _, present := srv.CallsTo("problem.get")[0].Params["selectHosts"]; present {
		t.Error("problem.get must not be sent selectHosts; the API rejects it")
	}
}

func TestSuppressedProblemsAreShownByDefaultAndNamed(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("problem.get", []any{
		zbxtest.Problem("100", "500", "Disk full", "4", map[string]any{
			"suppressed": "1",
			"suppression_data": []any{map[string]any{
				"maintenanceid": "7", "suppress_until": "1790000000", "userid": "0",
			}},
		}),
	})
	srv.Reply("trigger.get", []any{})
	srv.Reply("maintenance.get", []any{map[string]any{"maintenanceid": "7", "name": "planned move"}})
	svc := newService(t, srv)

	problems, _, err := svc.ListProblems(context.Background(), service.ProblemQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListProblems: %v", err)
	}
	if len(problems) != 1 || !problems[0].Suppressed {
		t.Fatalf("suppressed problems must be returned by default: %+v", problems)
	}
	if len(problems[0].SuppressedBy) != 1 || problems[0].SuppressedBy[0].MaintenanceName != "planned move" {
		t.Errorf("the suppressing window must be named: %+v", problems[0].SuppressedBy)
	}
	// Not asking Zabbix to filter is what keeps suppressed problems visible.
	if _, present := srv.CallsTo("problem.get")[0].Params["suppressed"]; present {
		t.Error("the default query must not filter on suppression")
	}
}

func TestExcludeSuppressedIsOptIn(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("problem.get", []any{})
	svc := newService(t, srv)
	if _, _, err := svc.ListProblems(context.Background(),
		service.ProblemQuery{Limit: 10, ExcludeSuppressed: true}); err != nil {
		t.Fatalf("ListProblems: %v", err)
	}
	if got := srv.CallsTo("problem.get")[0].Params["suppressed"]; got != false {
		t.Errorf("suppressed filter = %v, want false", got)
	}
}

func TestLatestValuesQueryHistoryWithTheItemValueType(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	srv.Reply("item.get", []any{
		zbxtest.Item("1", "10", "CPU idle", "system.cpu.util[,idle]", "0", map[string]any{"units": "%"}),
		zbxtest.Item("2", "10", "Uptime", "system.uptime", "3", map[string]any{"units": "uptime"}),
	})
	srv.Handle("history.get", func(params map[string]any) (any, error) {
		switch params["history"] {
		case float64(0):
			return []any{map[string]any{"itemid": "1", "clock": nowClock(), "value": "74.16", "ns": "0"}}, nil
		case float64(3):
			return []any{map[string]any{"itemid": "2", "clock": nowClock(), "value": "3600", "ns": "0"}}, nil
		}
		// Zabbix defaults history to 3 and returns nothing useful for a float
		// item; a caller that omits the type sees silence, not an error.
		return []any{}, nil
	})
	svc := newService(t, srv)

	_, values, _, err := svc.LatestValues(context.Background(), service.ItemQuery{Host: "web01", Limit: 10})
	if err != nil {
		t.Fatalf("LatestValues: %v", err)
	}
	if len(values) != 2 {
		t.Fatalf("got %d values", len(values))
	}
	byKey := map[string]service.Value{}
	for _, v := range values {
		byKey[v.Key] = v
	}
	if got := byKey["system.cpu.util[,idle]"]; got.Value != "74.16%" || got.NoData {
		t.Errorf("float item = %+v", got)
	}
	if got := byKey["system.uptime"]; got.Value != "1h0m" {
		t.Errorf("uptime formatting = %q", got.Value)
	}
	for _, call := range srv.CallsTo("history.get") {
		if _, present := call.Params["history"]; !present {
			t.Error("history.get was called without an explicit history type")
		}
	}
}

func nowClock() string {
	return itoa64(time.Now().Unix())
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestItemsThatDoNotCollectOnScheduleAreNotCalledStale(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	srv.Reply("item.get", []any{
		// A trapper item arrives when something sends it; silence is normal.
		zbxtest.Item("1", "10", "Sent metric", "custom.trap", "0", map[string]any{"type": "2", "delay": "0"}),
		zbxtest.Item("2", "10", "Polled metric", "agent.ping", "3", map[string]any{"delay": "1m"}),
	})
	srv.Reply("history.get", []any{})
	svc := newService(t, srv)

	_, values, _, err := svc.LatestValues(context.Background(), service.ItemQuery{Host: "web01", Limit: 10})
	if err != nil {
		t.Fatalf("LatestValues: %v", err)
	}
	for _, v := range values {
		switch v.Key {
		case "custom.trap":
			if v.Stale {
				t.Error("a trapper item with no data must not be reported as stale")
			}
		case "agent.ping":
			if !v.Stale {
				t.Error("a scheduled item with no data must be reported as stale")
			}
		}
	}
}

func TestParseWindowAcceptsOperatorDurations(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"30m", 30 * time.Minute},
		{"2h", 2 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
	} {
		got, err := service.ParseWindow(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseWindow(%q) = %v, %v", tc.in, got, err)
		}
	}
	for _, bad := range []string{"", "soon", "-1h", "0s", "5x", "2wd", "1e100d"} {
		if _, err := service.ParseWindow(bad); err == nil {
			t.Errorf("ParseWindow(%q) must fail", bad)
		}
	}
}

func TestResolveHostPrefersNumericIDOverFuzzyNames(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Handle("host.get", func(params map[string]any) (any, error) {
		if _, ok := params["hostids"]; ok {
			return []any{zbxtest.Host("123", "production", nil)}, nil
		}
		return []any{
			zbxtest.Host("10", "server-123-a", nil),
			zbxtest.Host("11", "server-123-b", nil),
		}, nil
	})
	svc := newService(t, srv)

	host, err := svc.ResolveHost(context.Background(), "123")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host.ID != "123" || host.Name != "production" {
		t.Fatalf("resolved %+v, want host ID 123", host)
	}
	if calls := srv.CallsTo("host.get"); len(calls) != 1 {
		t.Fatalf("host.get called %d times; numeric ID should not fall through to fuzzy search", len(calls))
	}
}

func TestPartialHistoryNamesTheFailedSeries(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	srv.Reply("item.get", []any{
		zbxtest.Item("1", "10", "CPU idle", "system.cpu.util[,idle]", "0", nil),
		zbxtest.Item("2", "10", "Uptime", "system.uptime", "3", nil),
	})
	srv.Handle("history.get", func(params map[string]any) (any, error) {
		ids, _ := params["itemids"].([]any)
		if len(ids) > 0 && ids[0] == "2" {
			return nil, &zbxtest.APIError{Code: -32500, Message: "Internal error.", Data: "database is down"}
		}
		return []any{map[string]any{"itemid": "1", "clock": nowClock(), "value": "74.16"}}, nil
	})
	svc := newService(t, srv)

	_, series, err := svc.History(context.Background(), service.HistoryQuery{Host: "web01", Limit: 10})
	if err != nil {
		t.Fatalf("a partial history failure must preserve successful series: %v", err)
	}
	byKey := map[string]service.Series{}
	for _, s := range series {
		byKey[s.Key] = s
	}
	if byKey["system.uptime"].ReadError == "" {
		t.Fatal("failed series has no read_error")
	}
	if len(byKey["system.cpu.util[,idle]"].Points) != 1 {
		t.Fatal("successful series was lost")
	}
}

func TestAckMaskIsBuiltFromNamedOperations(t *testing.T) {
	// The 7.4 reference lists "6 - acknowledge event" but its own example
	// computes 34 for acknowledge plus suppress, which is 32 plus 2.
	// Acknowledge is bit 2; assembling the mask from names avoids the trap.
	for _, tc := range []struct {
		ops  []service.AckOperation
		want int
	}{
		{[]service.AckOperation{service.AckAcknowledge}, 2},
		{[]service.AckOperation{service.AckAcknowledge, service.AckMessage}, 6},
		{[]service.AckOperation{service.AckAcknowledge, service.AckSuppress}, 34},
		{[]service.AckOperation{service.AckClose}, 1},
		{[]service.AckOperation{service.AckAcknowledge, service.AckAcknowledge}, 2},
	} {
		got, err := service.AckMask(tc.ops)
		if err != nil {
			t.Errorf("AckMask(%v): %v", tc.ops, err)
			continue
		}
		if got != tc.want {
			t.Errorf("AckMask(%v) = %d, want %d", tc.ops, got, tc.want)
		}
	}
	if _, err := service.AckMask(nil); err == nil {
		t.Error("an empty operation list must fail")
	}
	if _, err := service.AckMask([]service.AckOperation{"explode"}); err == nil {
		t.Error("an unknown operation must fail")
	}
	if _, err := service.AckMask([]service.AckOperation{service.AckAcknowledge, service.AckUnacknowledge}); err == nil {
		t.Error("contradictory operations must fail")
	}
}

func TestSeverityAccepatsNamesAndNumbers(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"high", 4}, {"4", 4}, {"disaster", 5}, {"warning", 2}, {"Average", 3},
	} {
		got, err := service.SeverityValue(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("SeverityValue(%q) = %d, %v", tc.in, got, err)
		}
	}
	for _, bad := range []string{"", "9", "urgent"} {
		if _, err := service.SeverityValue(bad); err == nil {
			t.Errorf("SeverityValue(%q) must fail", bad)
		}
	}
}

func TestAuthenticationFailureIsReportedWithoutTheToken(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Fail("host.get", -32602, "Invalid params.", "Not authorised.")
	svc := newService(t, srv)

	_, _, err := svc.ListHosts(context.Background(), service.HostQuery{Limit: 5})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "test-token") {
		t.Fatalf("the error leaked the token: %v", err)
	}
	if !strings.Contains(err.Error(), "rejected the configured API token") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestExpireRefusesAWindowThatHasNotStarted(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	future := time.Now().Add(24 * time.Hour).Unix()
	srv.Reply("maintenance.get", []any{map[string]any{
		"maintenanceid": "7", "name": "planned move", "maintenance_type": "0",
		"active_since": itoa64(future), "active_till": itoa64(future + 7200),
		"hosts": []any{}, "hostgroups": []any{}, "timeperiods": []any{},
	}})
	svc := newService(t, srv)

	// Ending a window that has not begun would otherwise compute an end five
	// minutes after a start that is still in the future, quietly turning the
	// cancellation into a shorter future window.
	_, err := svc.PlanMaintenanceExpire(context.Background(), "prod", "7")
	if err == nil {
		t.Fatal("expiring a window that has not started must be refused")
	}
	var e *errs.E
	if !errors.As(err, &e) || !strings.Contains(e.Suggestion, "delete") {
		t.Errorf("the error should point at deletion instead: %v", err)
	}
}

func TestExpireEndsAnActiveWindow(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	since := time.Now().Add(-2 * time.Hour).Unix()
	srv.Reply("maintenance.get", []any{map[string]any{
		"maintenanceid": "7", "name": "in progress", "maintenance_type": "0",
		"active_since": itoa64(since), "active_till": itoa64(since + 86400),
		"hosts": []any{}, "hostgroups": []any{}, "timeperiods": []any{},
	}})
	svc := newService(t, srv)

	plan, err := svc.PlanMaintenanceExpire(context.Background(), "prod", "7")
	if err != nil {
		t.Fatalf("PlanMaintenanceExpire: %v", err)
	}
	till, ok := plan.Params["active_till"].(int64)
	if !ok {
		t.Fatalf("active_till = %T", plan.Params["active_till"])
	}
	if till > time.Now().Unix()+60 {
		t.Errorf("the window must end now, not in the future: %d", till)
	}
}

func TestHostGroupLookupPrefersAnExactName(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("hostgroup.get", []any{
		map[string]any{"groupid": "1", "name": "Linux servers"},
		map[string]any{"groupid": "2", "name": "Linux"},
		map[string]any{"groupid": "3", "name": "Embedded Linux"},
	})
	srv.Reply("problem.get", []any{})
	srv.Reply("trigger.get", []any{})
	svc := newService(t, srv)

	if _, _, err := svc.ListProblems(context.Background(),
		service.ProblemQuery{Group: "Linux", Limit: 10}); err != nil {
		t.Fatalf("ListProblems: %v", err)
	}
	// A group literally named "Linux" must not silently widen into every
	// group whose name contains it.
	ids, _ := srv.CallsTo("problem.get")[0].Params["groupids"].([]any)
	if len(ids) != 1 || ids[0] != "2" {
		t.Errorf("groupids = %v, want only the exactly named group", ids)
	}
}

func TestUnixtimeZeroIsNotRenderedAsTheEpoch(t *testing.T) {
	if got := service.FormatValue("0", "unixtime", "3"); got == "1970-01-01T00:00:00Z" {
		t.Errorf("a zero timestamp must not read as a real date: %q", got)
	}
}

// The write path matters more than the read path here: this list decides
// which hosts stop alerting.
func TestMaintenanceCreatePrefersAnExactGroupName(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("hostgroup.get", []any{
		map[string]any{"groupid": "1", "name": "Linux servers"},
		map[string]any{"groupid": "2", "name": "Linux"},
		map[string]any{"groupid": "3", "name": "Embedded Linux"},
	})
	svc := newService(t, srv)

	plan, err := svc.PlanMaintenanceCreate(context.Background(), "test", service.MaintenanceCreateRequest{
		Name:     "patching",
		Groups:   []string{"Linux"},
		Duration: time.Hour,
	})
	if err != nil {
		t.Fatalf("PlanMaintenanceCreate: %v", err)
	}
	groups, _ := plan.Params["groups"].([]map[string]any)
	if len(groups) != 1 || groups[0]["groupid"] != "2" {
		t.Errorf("groups = %v, want only the exactly named group", plan.Params["groups"])
	}
}

// "No data" in this tool reads as "your monitoring is broken". A read that
// failed must not be reported as one.
func TestAFailedHistoryReadIsNotReportedAsNoData(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	srv.Reply("item.get", []any{
		zbxtest.Item("1", "10", "CPU idle", "system.cpu.util[,idle]", "0", map[string]any{"units": "%"}),
		zbxtest.Item("2", "10", "Uptime", "system.uptime", "3", map[string]any{"units": "uptime"}),
	})
	srv.Handle("history.get", func(params map[string]any) (any, error) {
		ids, _ := params["itemids"].([]any)
		if len(ids) > 0 && ids[0] == "2" {
			return nil, &zbxtest.APIError{Code: -32500, Message: "Internal error.", Data: "database is down"}
		}
		return []any{map[string]any{"itemid": "1", "clock": nowClock(), "value": "74.16", "ns": "0"}}, nil
	})
	svc := newService(t, srv)

	_, values, _, err := svc.LatestValues(context.Background(), service.ItemQuery{Host: "web01", Limit: 10})
	if err != nil {
		t.Fatalf("a partial failure must not fail the whole call: %v", err)
	}
	byKey := map[string]service.Value{}
	for _, v := range values {
		byKey[v.Key] = v
	}
	failed := byKey["system.uptime"]
	if failed.NoData {
		t.Error("an item whose history could not be read was reported as having no data")
	}
	if failed.ReadError == "" {
		t.Error("the failure was not reported at all")
	}
	if ok := byKey["system.cpu.util[,idle]"]; ok.Value == "" || ok.NoData {
		t.Errorf("the item that did answer was lost: %+v", ok)
	}
}

func TestAlertFallbackUnionsGroupAndDirectRecipients(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("event.get", []any{map[string]any{
		"eventid": "42", "clock": nowClock(), "name": "Disk full", "severity": "4",
		"acknowledged": "0", "value": "1", "objectid": "9", "r_eventid": "0",
		"hosts": []any{}, "suppression_data": []any{},
	}})
	srv.Reply("alert.get", []any{})
	srv.Handle("action.get", func(map[string]any) (any, error) {
		return []any{map[string]any{
			"actionid": "1", "name": "Notify", "status": "0", "eventsource": "0",
			"operations": []any{map[string]any{
				"operationtype": "0",
				"opmessage":     map[string]any{"mediatypeid": "1"},
				"opmessage_grp": []any{map[string]any{"usrgrpid": "10"}},
				"opmessage_usr": []any{map[string]any{"userid": "20"}},
			}},
		}}, nil
	})
	srv.Reply("mediatype.get", []any{map[string]any{"mediatypeid": "1", "name": "Email", "status": "0"}})
	srv.Handle("user.get", func(params map[string]any) (any, error) {
		if _, hasGroups := params["usrgrpids"]; hasGroups {
			if _, hasUsers := params["userids"]; hasUsers {
				t.Fatal("group and user filters must not be intersected in one user.get call")
			}
			return []any{recipient("10", "group-user")}, nil
		}
		return []any{recipient("20", "direct-user")}, nil
	})

	exp, err := newService(t, srv).ExplainAlert(context.Background(), "42")
	if err != nil {
		t.Fatalf("ExplainAlert: %v", err)
	}
	if len(srv.CallsTo("user.get")) != 2 || len(exp.Recipients) != 2 {
		t.Fatalf("recipient union failed: calls=%d recipients=%+v", len(srv.CallsTo("user.get")), exp.Recipients)
	}
	if !exp.Partial || len(exp.Warnings) == 0 || !strings.Contains(exp.Warnings[0], "candidates only") {
		t.Fatalf("heuristic fallback must be explicit: partial=%v warnings=%v", exp.Partial, exp.Warnings)
	}
}

func TestAlertReadFailureIsNotReportedAsNoAttempts(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("event.get", []any{map[string]any{
		"eventid": "42", "clock": nowClock(), "name": "Disk full", "severity": "4",
		"acknowledged": "0", "value": "1", "objectid": "9", "r_eventid": "0",
		"hosts": []any{}, "suppression_data": []any{},
	}})
	srv.Fail("alert.get", -32500, "Internal error.", "database is down")

	exp, err := newService(t, srv).ExplainAlert(context.Background(), "42")
	if err != nil {
		t.Fatalf("ExplainAlert: %v", err)
	}
	for _, finding := range exp.Findings {
		if strings.Contains(finding, "no delivery attempt") || strings.Contains(finding, "no obstacle") {
			t.Fatalf("an unread result was reported as an empty result: %q", finding)
		}
	}
	if !exp.Partial || len(exp.Warnings) == 0 || len(srv.CallsTo("action.get")) != 0 {
		t.Fatalf("read failure was not kept distinct: partial=%v warnings=%v action calls=%d",
			exp.Partial, exp.Warnings, len(srv.CallsTo("action.get")))
	}
}

func TestAlertFallbackHonoursSelectedAndDisabledMediaTypes(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("event.get", []any{map[string]any{
		"eventid": "42", "clock": nowClock(), "name": "Disk full", "severity": "4",
		"acknowledged": "0", "value": "1", "objectid": "9", "r_eventid": "0",
		"hosts": []any{}, "suppression_data": []any{},
	}})
	srv.Reply("alert.get", []any{})
	srv.Handle("action.get", func(map[string]any) (any, error) {
		return []any{map[string]any{
			"actionid": "1", "name": "Notify by SMS", "status": "0", "eventsource": "0",
			"operations": []any{map[string]any{
				"operationtype": "0", "opmessage": map[string]any{"mediatypeid": "2"},
				"opmessage_usr": []any{map[string]any{"userid": "20"}}, "opmessage_grp": []any{},
			}},
		}}, nil
	})
	srv.Reply("mediatype.get", []any{
		map[string]any{"mediatypeid": "1", "name": "Email", "status": "0"},
		map[string]any{"mediatypeid": "2", "name": "SMS", "status": "1"},
	})
	srv.Reply("user.get", []any{map[string]any{
		"userid": "20", "username": "operator",
		"medias": []any{
			map[string]any{"mediatypeid": "1", "sendto": "operator@example.com", "active": "0", "severity": "63", "period": "1-7,00:00-24:00"},
			map[string]any{"mediatypeid": "2", "sendto": "+12025550123", "active": "0", "severity": "63", "period": "1-7,00:00-24:00"},
		},
	}})

	exp, err := newService(t, srv).ExplainAlert(context.Background(), "42")
	if err != nil {
		t.Fatalf("ExplainAlert: %v", err)
	}
	if len(exp.Recipients) != 1 || exp.Recipients[0].MediaType != "SMS" || exp.Recipients[0].MediaEnabled {
		t.Fatalf("operation media selection/global status was lost: %+v", exp.Recipients)
	}
	if !containsText(exp.Findings, "no action-selected user media") {
		t.Fatalf("disabled selected media type was not diagnosed: %v", exp.Findings)
	}
}

func TestAlertFallbackReportsTruncatedConfiguration(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("event.get", []any{map[string]any{
		"eventid": "42", "clock": nowClock(), "name": "Disk full", "severity": "4",
		"acknowledged": "0", "value": "1", "objectid": "9", "r_eventid": "0",
		"hosts": []any{}, "suppression_data": []any{},
	}})
	alerts := make([]any, 101)
	for i := range alerts {
		alerts[i] = map[string]any{"alertid": itoa64(int64(i + 1)), "status": "1", "clock": nowClock(), "alerttype": "0"}
	}
	srv.Reply("alert.get", alerts)

	exp, err := newService(t, srv).ExplainAlert(context.Background(), "42")
	if err != nil {
		t.Fatalf("ExplainAlert: %v", err)
	}
	if len(exp.Attempts) != 100 || !exp.Partial || !containsText(exp.Warnings, "truncated at 100") {
		t.Fatalf("attempt truncation was hidden: attempts=%d partial=%v warnings=%v", len(exp.Attempts), exp.Partial, exp.Warnings)
	}
}

func TestAlertFallbackReportsTruncatedActionsAndRecipients(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("event.get", []any{map[string]any{
		"eventid": "42", "clock": nowClock(), "name": "Disk full", "severity": "4",
		"acknowledged": "0", "value": "1", "objectid": "9", "r_eventid": "0",
		"hosts": []any{}, "suppression_data": []any{},
	}})
	srv.Reply("alert.get", []any{})
	actions := make([]any, 51)
	actions[0] = map[string]any{
		"actionid": "1", "name": "Notify", "status": "0", "eventsource": "0",
		"operations": []any{map[string]any{
			"operationtype": "0", "opmessage": map[string]any{"mediatypeid": "1"},
			"opmessage_usr": []any{map[string]any{"userid": "20"}}, "opmessage_grp": []any{},
		}},
	}
	for i := 1; i < len(actions); i++ {
		actions[i] = map[string]any{"actionid": itoa64(int64(i + 1)), "name": "Disabled", "status": "1", "eventsource": "0", "operations": []any{}}
	}
	srv.Handle("action.get", func(map[string]any) (any, error) { return actions, nil })
	srv.Reply("mediatype.get", []any{map[string]any{"mediatypeid": "1", "name": "Email", "status": "0"}})
	users := make([]any, 101)
	for i := range users {
		users[i] = recipient(itoa64(int64(i+1)), "operator")
	}
	srv.Reply("user.get", users)

	exp, err := newService(t, srv).ExplainAlert(context.Background(), "42")
	if err != nil {
		t.Fatalf("ExplainAlert: %v", err)
	}
	if !exp.Partial || !containsText(exp.Warnings, "actions were truncated") || !containsText(exp.Warnings, "recipients were truncated") {
		t.Fatalf("configuration truncation was hidden: partial=%v warnings=%v", exp.Partial, exp.Warnings)
	}
}

func TestHistoryPointsAreNeverNullForReturnedSeries(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("host.get", []any{zbxtest.Host("10", "web01", nil)})
	srv.Reply("item.get", []any{
		zbxtest.Item("1", "10", "Binary", "binary", "5", nil),
		zbxtest.Item("2", "10", "Broken", "broken", "0", nil),
		zbxtest.Item("3", "10", "Working", "working", "0", nil),
	})
	srv.Handle("history.get", func(params map[string]any) (any, error) {
		ids, _ := params["itemids"].([]any)
		if len(ids) > 0 && ids[0] == "2" {
			return nil, &zbxtest.APIError{Code: -32500, Message: "Internal error.", Data: "database is down"}
		}
		return []any{map[string]any{"itemid": "3", "clock": nowClock(), "value": "1"}}, nil
	})

	_, series, err := newService(t, srv).History(context.Background(), service.HistoryQuery{Host: "web01", Items: 3})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	for _, itemSeries := range series {
		if itemSeries.Points == nil {
			t.Errorf("%s points are nil and would serialize as null", itemSeries.Key)
		}
	}
}

func containsText(values []string, fragment string) bool {
	for _, value := range values {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}

func recipient(id, username string) map[string]any {
	return map[string]any{
		"userid": id, "username": username,
		"medias": []any{map[string]any{
			"mediatypeid": "1", "sendto": username + "@example.com", "active": "0",
			"severity": "63", "period": "1-7,00:00-24:00",
		}},
	}
}

// Every message operation of every action produces a candidate, and each
// candidate resolved its recipients with its own API calls. On a site with a
// few dozen notifying actions that walk runs past the client timeout, so the
// explanation answers nothing instead of answering slowly.
func TestAlertWhyDoesNotRefetchTheSameRecipientsPerAction(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Reply("event.get", []any{map[string]any{
		"eventid": "42", "clock": nowClock(), "name": "Disk full", "severity": "4",
		"acknowledged": "0", "value": "1", "objectid": "9", "r_eventid": "0",
		"hosts": []any{}, "suppression_data": []any{},
	}})
	srv.Reply("alert.get", []any{})
	// Twenty actions that all notify the same on-call group, which is what a
	// real installation looks like.
	actions := make([]any, 0, 20)
	for i := 1; i <= 20; i++ {
		actions = append(actions, map[string]any{
			"actionid": itoaTest(i), "name": "Notify " + itoaTest(i), "status": "0", "eventsource": "0",
			"operations": []any{map[string]any{
				"operationtype": "0", "opmessage": map[string]any{"mediatypeid": "1"},
				"opmessage_usr": []any{}, "opmessage_grp": []any{map[string]any{"usrgrpid": "7"}},
			}},
		})
	}
	srv.Reply("action.get", actions)
	srv.Reply("mediatype.get", []any{map[string]any{"mediatypeid": "1", "name": "Email", "status": "0"}})
	srv.Reply("user.get", []any{map[string]any{
		"userid": "20", "username": "operator",
		"medias": []any{map[string]any{
			"mediatypeid": "1", "sendto": "operator@example.com",
			"active": "0", "severity": "63", "period": "1-7,00:00-24:00",
		}},
	}})

	if _, err := newService(t, srv).ExplainAlert(context.Background(), "42"); err != nil {
		t.Fatalf("ExplainAlert: %v", err)
	}
	calls := len(srv.CallsTo("user.get"))
	if calls == 0 {
		t.Fatal("no recipients were resolved at all, so this proves nothing")
	}
	if calls > 2 {
		t.Errorf("user.get called %d times for one repeated group; the lookup is not being reused", calls)
	}
}

func itoaTest(n int) string { return strconv.Itoa(n) }

// A host ID wins over a name, but a host whose visible name is a numeric asset
// tag must still resolve: Zabbix rejects a non-existent ID outright, and
// treating that refusal as failure made the host unreachable by name.
func TestResolveHostFallsBackWhenANumericNameIsNotAnID(t *testing.T) {
	srv := zbxtest.New(t, "7.4.10")
	srv.Handle("host.get", func(params map[string]any) (any, error) {
		if _, byID := params["hostids"]; byID {
			return nil, &zbxtest.APIError{
				Code: -32602, Message: "Invalid params.",
				Data: `Invalid parameter "/hostids/1": a number is too large.`,
			}
		}
		return []any{zbxtest.Host("10", "1000000000000000000001", nil)}, nil
	})

	host, err := newService(t, srv).ResolveHost(context.Background(), "1000000000000000000001")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if host.Name != "1000000000000000000001" {
		t.Errorf("resolved %q", host.Name)
	}
}
