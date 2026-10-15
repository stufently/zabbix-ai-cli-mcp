package safety

import (
	"strings"
	"testing"
)

func TestConstrainedUpdateFields(t *testing.T) {
	for _, tc := range []struct {
		method  string
		params  any
		allowed bool
		risk    Risk
		reason  string
	}{
		{"mediatype.update", map[string]any{"mediatypeid": "1", "status": "0"}, true, RiskWrite, ""},
		{"mediatype.update", map[string]any{"mediatypeid": "1", "status": 1}, true, RiskDestructive, ""},
		{"mediatype.update", map[string]any{"mediatypeid": "1", "status": "0", "exec_path": "evil"}, false, "", "exec_path"},
		{"mediatype.update", map[string]any{"mediatypeid": "1", "status": "0", "parameters": []any{}}, false, "", "parameters"},
		{"mediatype.update", map[string]any{"mediatypeid": "1", "status": "0", "smtp_server": "evil"}, false, "", "smtp_server"},
		{"mediatype.update", map[string]any{"mediatypeid": "1", "status": "0", "script": "evil"}, false, "", "script"},
		{"mediatype.update", map[string]any{"mediatypeid": "1"}, false, "", "status"},
		{"mediatype.update", map[string]any{"status": "0"}, false, "", "mediatypeid"},
		{"mediatype.update", map[string]any{"mediatypeid": "1", "status": 2}, false, "", "status"},
		{"mediatype.update", []any{map[string]any{"mediatypeid": "1", "status": "0"}, map[string]any{"mediatypeid": "2", "status": "0", "exec_path": "evil"}}, false, "", "exec_path"},
		{"discoveryrule.update", map[string]any{"itemid": "234350", "enabled_lifetime_type": "2"}, true, RiskWrite, ""},
		{"discoveryrule.update", map[string]any{"itemid": "234350", "lifetime_type": "2"}, true, RiskDestructive, ""},
		{"discoveryrule.update", map[string]any{"itemid": "234350", "lifetime_type": "1", "lifetime": "7d", "enabled_lifetime_type": "0", "enabled_lifetime": "1h"}, true, RiskWrite, ""},
		{"discoveryrule.update", map[string]any{"itemid": "234350", "enabled_lifetime_type": "2", "key_": "evil"}, false, "", "key_"},
		{"discoveryrule.update", map[string]any{"itemid": "234350", "enabled_lifetime_type": "2", "filter": map[string]any{}}, false, "", "filter"},
		{"discoveryrule.update", map[string]any{"itemid": "234350", "enabled_lifetime_type": "2", "delay": "1s"}, false, "", "delay"},
		{"discoveryrule.update", map[string]any{"itemid": "234350"}, false, "", "at least one"},
		{"discoveryrule.update", map[string]any{"itemid": "234350", "lifetime_type": 3}, false, "", "lifetime_type"},
	} {
		t.Run(tc.method+tc.reason, func(t *testing.T) {
			c := ClassifyCall(tc.method, tc.params)
			if c.Allowed != tc.allowed || c.Allowed && (c.Risk != tc.risk || c.Scope != ScopeConfiguration) || !c.Allowed && !strings.Contains(c.Reason, tc.reason) {
				t.Fatalf("classification = %+v", c)
			}
		})
	}
}
