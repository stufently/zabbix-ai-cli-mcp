package ops

import (
	"context"
	"strings"
	"time"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
)

func init() {
	register(
		maintenanceCreate(),
		maintenanceExtend(),
		maintenanceExpire(),
		maintenanceDelete(),
		eventsAcknowledge(),
		triggersDisable(),
		triggersEnable(),
		apiCall(),
	)
}

func maintenanceCreate() *opspec.Operation {
	return &opspec.Operation{
		Name:    "maintenance.create",
		CLI:     []string{"maintenance", "create"},
		Risk:    safety.RiskWrite,
		Scope:   safety.ScopeMaintenance,
		Summary: "Open a maintenance window over hosts or host groups.",
		Long: "Host names may be patterns, so a fleet can be silenced the way it is described: ms*, massivegrid*. " +
			"Every pattern must match something; one that matches nothing is an error rather than a quietly smaller window.",
		Params: []opspec.Param{
			{Name: "hosts", Type: opspec.TypeStringList, Positional: true,
				Description: "host names or patterns", Example: "ms*,dx1"},
			{Name: "groups", Type: opspec.TypeStringList, Description: "host group names or patterns"},
			{Name: "for", Type: opspec.TypeDuration, Required: true,
				Description: "how long the window lasts", Example: "2h"},
			{Name: "name", Type: opspec.TypeString, Description: "window name; generated from the hosts and duration if omitted"},
			{Name: "description", Type: opspec.TypeString, Description: "free-text note stored with the window"},
			{Name: "no_data_collection", Type: opspec.TypeBool,
				Description: "stop collecting data during the window; by default collection continues"},
			{Name: "start_in", Type: opspec.TypeDuration, Description: "delay the start, for example 30m"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			hosts := args.Strings("hosts")
			groups := args.Strings("groups")
			if len(hosts) == 0 && len(groups) == 0 {
				return nil, errs.Usage("name at least one host or host group")
			}
			duration := args.Duration("for")
			name := args.String("name")
			if name == "" {
				name = generatedWindowName(hosts, groups, duration)
			}
			start := time.Now()
			if d := args.Duration("start_in"); d > 0 {
				start = start.Add(d)
			}
			return env.Service.PlanMaintenanceCreate(ctx, env.Profile, service.MaintenanceCreateRequest{
				Name:        name,
				Hosts:       hosts,
				Groups:      groups,
				Duration:    duration,
				StartAt:     start,
				CollectData: !args.Bool("no_data_collection"),
				Description: args.String("description"),
			})
		},
	}
}

func generatedWindowName(hosts, groups []string, d time.Duration) string {
	subject := strings.Join(append(append([]string{}, hosts...), groups...), ", ")
	if len(subject) > 60 {
		subject = subject[:57] + "..."
	}
	return subject + " (" + service.HumanDuration(d) + ")"
}

func maintenanceExtend() *opspec.Operation {
	return &opspec.Operation{
		Name:    "maintenance.extend",
		CLI:     []string{"maintenance", "extend"},
		Risk:    safety.RiskWrite,
		Scope:   safety.ScopeMaintenance,
		Summary: "Push a maintenance window's end further out.",
		Params: []opspec.Param{
			{Name: "id", Type: opspec.TypeString, Required: true, Positional: true, Description: "maintenance identifier"},
			{Name: "by", Type: opspec.TypeDuration, Required: true, Description: "how much longer", Example: "24h"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			return env.Service.PlanMaintenanceExtend(ctx, env.Profile, args.String("id"), args.Duration("by"))
		},
	}
}

func maintenanceExpire() *opspec.Operation {
	return &opspec.Operation{
		Name:    "maintenance.expire",
		CLI:     []string{"maintenance", "expire"},
		Risk:    safety.RiskWrite,
		Scope:   safety.ScopeMaintenance,
		Summary: "End a maintenance window now, keeping its record.",
		Long:    "Preferred over deletion when the window should stop suppressing alerts but the history of it matters.",
		Params: []opspec.Param{
			{Name: "id", Type: opspec.TypeString, Required: true, Positional: true, Description: "maintenance identifier"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			return env.Service.PlanMaintenanceExpire(ctx, env.Profile, args.String("id"))
		},
	}
}

func maintenanceDelete() *opspec.Operation {
	return &opspec.Operation{
		Name:    "maintenance.delete",
		CLI:     []string{"maintenance", "delete"},
		Risk:    safety.RiskDestructive,
		Scope:   safety.ScopeMaintenance,
		Summary: "Remove a maintenance window permanently.",
		Params: []opspec.Param{
			{Name: "id", Type: opspec.TypeString, Required: true, Positional: true, Description: "maintenance identifier"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			return env.Service.PlanMaintenanceDelete(ctx, env.Profile, args.String("id"))
		},
	}
}

func eventsAcknowledge() *opspec.Operation {
	return &opspec.Operation{
		Name:    "events.acknowledge",
		CLI:     []string{"events", "acknowledge"},
		Risk:    safety.RiskWrite,
		Scope:   safety.ScopeAcknowledge,
		Summary: "Acknowledge an event, comment on it, close it or change its severity.",
		Long: "Operations are named rather than numbered. The underlying bitmask is easy to get wrong, " +
			"and the cost of getting it wrong is closing a problem when the intent was to comment on it.",
		Params: []opspec.Param{
			{Name: "event", Type: opspec.TypeString, Required: true, Positional: true, Description: "event identifier"},
			{Name: "operations", Type: opspec.TypeStringList, Default: []string{"ack"},
				Description: "one or more of: " + strings.Join(service.AckOperations, ", "), Example: "ack,message"},
			{Name: "message", Type: opspec.TypeString, Description: "text to attach"},
			{Name: "severity", Type: opspec.TypeString, Description: "new severity, required with the severity operation"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			raw := args.Strings("operations")
			if len(raw) == 0 {
				raw = []string{string(service.AckAcknowledge)}
			}
			ops := make([]service.AckOperation, 0, len(raw))
			for _, r := range raw {
				ops = append(ops, service.AckOperation(strings.ToLower(strings.TrimSpace(r))))
			}
			return env.Service.PlanAcknowledge(ctx, env.Profile, service.AcknowledgeRequest{
				EventID:    args.String("event"),
				Operations: ops,
				Message:    args.String("message"),
				Severity:   args.String("severity"),
			})
		},
	}
}

func triggersDisable() *opspec.Operation {
	return &opspec.Operation{
		Name:    "triggers.disable",
		CLI:     []string{"triggers", "disable"},
		Risk:    safety.RiskDestructive,
		Scope:   safety.ScopeConfiguration,
		Summary: "Disable a trigger so it stops raising problems.",
		Long: "Classed as destructive on purpose: a disabled trigger produces neither an alert nor a record " +
			"that it would have, which is indistinguishable from the fault never happening.",
		Params: []opspec.Param{
			{Name: "id", Type: opspec.TypeString, Required: true, Positional: true, Description: "trigger identifier"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			return env.Service.PlanTriggerState(ctx, env.Profile, args.String("id"), false)
		},
	}
}

func triggersEnable() *opspec.Operation {
	return &opspec.Operation{
		Name:    "triggers.enable",
		CLI:     []string{"triggers", "enable"},
		Risk:    safety.RiskWrite,
		Scope:   safety.ScopeConfiguration,
		Summary: "Re-enable a disabled trigger.",
		Params: []opspec.Param{
			{Name: "id", Type: opspec.TypeString, Required: true, Positional: true, Description: "trigger identifier"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			return env.Service.PlanTriggerState(ctx, env.Profile, args.String("id"), true)
		},
	}
}

// apiCall is the escape hatch. It is both a read and a write operation: a read
// method runs immediately, a write method produces a plan like any other.
//
// It exists because the alternative is not safety but invisibility. When the
// tool this replaces refused a write, the work was done anyway with a token
// copied out of a container, leaving no audit trail at all.
func apiCall() *opspec.Operation {
	return &opspec.Operation{
		Name:    "api.call",
		CLI:     []string{"api", "call"},
		MCPTool: "zabbix_api_call",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Call a Zabbix API method directly.",
		Long: "Read methods run immediately. Write methods produce a plan and go through approval like every other change. " +
			"A method that is not in the risk registry is refused rather than guessed at. " +
			"Over MCP this tool is read-only; writes are requested through the plan tool.",
		Params: []opspec.Param{
			{Name: "method", Type: opspec.TypeString, Required: true, Positional: true,
				Description: "API method, for example host.get", Example: "host.get"},
			{Name: "params", Type: opspec.TypeString,
				Description: "method parameters as JSON", Example: `{"output":["hostid","host"],"limit":5}`},
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			params, err := service.ParseParams(args.String("params"))
			if err != nil {
				return nil, err
			}
			r, err := env.Service.RawRead(ctx, args.String("method"), params)
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: r}
			res.Meta.Returned = 1
			res.Warn("raw API output is not projected or bounded by this tool; prefer a high-level command where one exists")
			return res, nil
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			params, err := service.ParseParams(args.String("params"))
			if err != nil {
				return nil, err
			}
			return env.Service.PlanRawCall(ctx, env.Profile, args.String("method"), params)
		},
		IsWrite: func(args *opspec.Args) bool {
			return safety.ClassifyMethod(args.String("method")).Risk != safety.RiskRead
		},
		Refuses: func(args *opspec.Args) error {
			method := args.String("method")
			class := safety.ClassifyMethod(method)
			if class.Allowed {
				return nil
			}
			return service.DeniedMethodError(method, class)
		},
	}
}
