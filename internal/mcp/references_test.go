package mcp_test

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
)

var toolReference = regexp.MustCompile(`zabbix_[a-z_]+[a-z]`)

// A description that names a tool the server did not register sends the model
// to a call that can only fail. Every mode registers a different set, so every
// mode is checked against its own tools/list.
func TestDescriptionsNameOnlyRegisteredTools(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(t *testing.T) *harness
		// mentioned are tools the mode must still point the model at, so a
		// fix that drops every reference does not pass for free.
		mentioned []string
	}{
		{"readonly", func(t *testing.T) *harness { return newHarness(t, true) }, nil},
		{"plan", func(t *testing.T) *harness { return newHarness(t, false) },
			[]string{"zabbix_plan_create"}},
		{"write", func(t *testing.T) *harness { return newWritableHarness(t) },
			[]string{"zabbix_plan_create", "zabbix_write"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.new(t)
			res, err := h.session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			registered := map[string]bool{}
			for _, tool := range res.Tools {
				registered[tool.Name] = true
			}
			texts := map[string]string{"server instructions": h.session.InitializeResult().Instructions}
			for _, tool := range res.Tools {
				schema, err := json.Marshal(tool.InputSchema)
				if err != nil {
					t.Fatalf("marshal schema of %s: %v", tool.Name, err)
				}
				texts[tool.Name] = tool.Description + "\n" + string(schema)
			}
			for where, s := range texts {
				for _, ref := range toolReference.FindAllString(s, -1) {
					if !registered[ref] {
						t.Errorf("%s names %s, which this mode does not register", where, ref)
					}
				}
			}
			maint := texts["zabbix_maintenance_list"]
			for _, want := range tc.mentioned {
				if !regexp.MustCompile(`\b` + want + `\b`).MatchString(maint) {
					t.Errorf("zabbix_maintenance_list does not route a change to %s:\n%s", want, maint)
				}
			}
			if tc.mentioned == nil && !regexp.MustCompile(`read-only`).MatchString(maint) {
				t.Errorf("zabbix_maintenance_list does not say a change is impossible here:\n%s", maint)
			}
		})
	}
}
