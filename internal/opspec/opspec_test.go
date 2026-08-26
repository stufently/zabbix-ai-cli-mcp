package opspec

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
)

func testOperation() *Operation {
	return &Operation{
		Name:  "problems.list",
		CLI:   []string{"problems", "list"},
		Risk:  safety.RiskRead,
		Scope: safety.ScopeRead,
		Params: []Param{
			{Name: "host", Type: TypeString, Description: "a host"},
			{Name: "limit", Type: TypeInt, Default: 50, Description: "row bound"},
			{Name: "since", Type: TypeDuration, Description: "a window"},
			{Name: "unacknowledged", Type: TypeBool, Description: "a flag"},
			{Name: "hosts", Type: TypeStringList, Description: "a list"},
			{Name: "severity", Type: TypeString, Enum: []string{"high", "disaster"}, Description: "a level"},
			{Name: "event", Type: TypeString, Required: true, Description: "an identifier"},
		},
	}
}

func TestBindRejectsUnknownParametersAndNamesTheRealOnes(t *testing.T) {
	op := testOperation()
	_, err := op.Bind(map[string]any{"event": "1", "selectHosts": "extend"})
	if err == nil {
		t.Fatal("an unknown parameter must be refused")
	}
	// Guessed parameter names were a recurring failure against the tool this
	// replaces, answered with nothing more useful than "unexpected parameter".
	for _, want := range []string{"selectHosts", "host", "limit", "severity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must mention %q: %s", want, err)
		}
	}
}

func TestBindRequiresRequiredParameters(t *testing.T) {
	op := testOperation()
	if _, err := op.Bind(map[string]any{}); err == nil {
		t.Fatal("a missing required parameter must be refused")
	}
	if _, err := op.Bind(map[string]any{"event": "   "}); err == nil {
		t.Fatal("a blank required parameter must be refused")
	}
}

func TestBindCoercesTypes(t *testing.T) {
	op := testOperation()
	args, err := op.Bind(map[string]any{
		"event":          "100",
		"limit":          float64(10), // JSON numbers arrive as float64
		"since":          "24h",
		"unacknowledged": true,
		"hosts":          []any{"a", "b"},
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if args.Int("limit") != 10 {
		t.Errorf("limit = %d", args.Int("limit"))
	}
	if args.Duration("since") != 24*time.Hour {
		t.Errorf("since = %v", args.Duration("since"))
	}
	if !args.Bool("unacknowledged") {
		t.Error("unacknowledged was lost")
	}
	if got := args.Strings("hosts"); len(got) != 2 || got[0] != "a" {
		t.Errorf("hosts = %v", got)
	}
}

func TestBindRejectsIntegerOutsidePlatformRange(t *testing.T) {
	op := &Operation{
		Name: "test.range", CLI: []string{"test", "range"},
		Params: []Param{{Name: "count", Type: TypeInt}},
	}
	for _, raw := range []any{math.Inf(1), math.Inf(-1), float64(math.MaxInt64)} {
		if _, err := op.Bind(map[string]any{"count": raw}); err == nil {
			t.Errorf("Bind accepted out-of-range integer %v", raw)
		}
	}
}

func TestDefaultsApplyWhenAbsent(t *testing.T) {
	op := testOperation()
	args, err := op.Bind(map[string]any{"event": "1"})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if args.Int("limit") != 50 {
		t.Errorf("the declared default must apply: got %d", args.Int("limit"))
	}
	if args.Has("host") {
		t.Error("an absent parameter must not be reported as present")
	}
}

func TestEnumIsEnforced(t *testing.T) {
	op := testOperation()
	if _, err := op.Bind(map[string]any{"event": "1", "severity": "urgent"}); err == nil {
		t.Fatal("a value outside the enum must be refused")
	}
	if _, err := op.Bind(map[string]any{"event": "1", "severity": "HIGH"}); err != nil {
		t.Errorf("enum matching must ignore case: %v", err)
	}
}

func TestBadDurationIsRefusedWithGuidance(t *testing.T) {
	op := testOperation()
	_, err := op.Bind(map[string]any{"event": "1", "since": "soon"})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "7d") {
		t.Errorf("the error should show the accepted forms: %s", err)
	}
}

func TestInputSchemaIsValidJSONSchema(t *testing.T) {
	op := testOperation()
	schema := op.InputSchema()
	if schema["type"] != "object" {
		t.Errorf("type = %v", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Error("the schema must reject unknown properties")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing")
	}
	if len(props) != len(op.Params) {
		t.Errorf("schema describes %d properties, the operation declares %d", len(props), len(op.Params))
	}
	limit := props["limit"].(map[string]any)
	if limit["type"] != "integer" || limit["default"] != 50 {
		t.Errorf("limit property = %v", limit)
	}
	hosts := props["hosts"].(map[string]any)
	if hosts["type"] != "array" {
		t.Errorf("a list must be described as an array: %v", hosts)
	}
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "event" {
		t.Errorf("required = %v", required)
	}
}

func TestWritesReflectsThePlanFunction(t *testing.T) {
	read := testOperation()
	if read.Writes(&Args{}) {
		t.Error("an operation with no plan function must not be a write")
	}
	write := testOperation()
	write.Plan = func(context.Context, *Env, *Args) (*safety.Plan, error) { return nil, nil }
	if !write.Writes(&Args{}) {
		t.Error("an operation with a plan function is a write")
	}
	// The raw escape hatch decides per call, from the method it is handed.
	hybrid := testOperation()
	hybrid.Plan = write.Plan
	hybrid.IsWrite = func(*Args) bool { return false }
	if hybrid.Writes(&Args{}) {
		t.Error("IsWrite must override the default")
	}
}
