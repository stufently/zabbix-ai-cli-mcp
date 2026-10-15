package ops

import (
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
	"strings"
	"testing"
)

func TestAPIParamsAcceptObjectAndString(t *testing.T) {
	op, _ := Lookup("api.call")
	for _, v := range []any{map[string]any{"output": []any{"hostid"}}, `{"output":["hostid"]}`} {
		args, err := op.Bind(map[string]any{"method": "host.get", "params": v})
		if err != nil {
			t.Fatal(err)
		}
		p, err := service.ParseParams(args.String("params"))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(map[string]any); !ok {
			t.Fatalf("params = %T", p)
		}
	}
}

func TestAPIParamsRejectInvalidJSONString(t *testing.T) {
	op, _ := Lookup("api.call")
	if _, err := op.Bind(map[string]any{"method": "host.get", "params": `{"output":`}); err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("invalid JSON accepted at bind: %v", err)
	}
}
