package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/output"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
)

// limitParam is the shared bound on how much an operation may return. Every
// list operation carries one, because an unbounded answer is how a single
// query fills an agent's context window.
// maxLimit is the ceiling every list operation shares. It exists so that a
// caller cannot ask for an answer larger than the context it has to read it
// in, whatever the operation's own default is.
const maxLimit = 500

func limitParam(def int) opspec.Param {
	min, max := opspec.IntRange(1, maxLimit)
	return opspec.Param{
		Name: "limit", Type: opspec.TypeInt, Default: def,
		Min: min, Max: max,
		Description: fmt.Sprintf("maximum rows to return, 1 to %d (default %d); the result reports whether it was truncated", maxLimit, def),
	}
}

func init() {
	register(
		problemsList(),
		problemsGet(),
		hostList(),
		hostGet(),
		hostStatus(),
		hostInvestigate(),
		metricsLatest(),
		metricsHistory(),
		alertWhy(),
		resolveAlert(),
		unreachable(),
		triggersList(),
		maintenanceList(),
	)
}

func problemsList() *opspec.Operation {
	return &opspec.Operation{
		Name:    "problems.list",
		CLI:     []string{"problems", "list"},
		MCPTool: "zabbix_problems",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "List active Zabbix problems, newest first.",
		Long: "Suppressed problems are included by default and say which maintenance window hides them. " +
			"Hiding them silently is how an outage goes unnoticed for weeks.",
		Params: []opspec.Param{
			{Name: "host", Type: opspec.TypeString, Description: "limit to one host; matching is fuzzy", Example: "web01"},
			{Name: "group", Type: opspec.TypeString, Description: "limit to a host group"},
			{Name: "severity", Type: opspec.TypeString, Description: "minimum severity, as a name or 0-5",
				Enum: []string{"not classified", "information", "warning", "average", "high", "disaster"}},
			{Name: "since", Type: opspec.TypeDuration, Description: "only problems that started within this window", Example: "24h"},
			{Name: "unacknowledged", Type: opspec.TypeBool, Description: "only problems nobody has acknowledged"},
			{Name: "exclude_suppressed", Type: opspec.TypeBool, Description: "hide problems inside a maintenance window (they are shown by default)"},
			limitParam(50),
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			q := service.ProblemQuery{
				Host:               args.String("host"),
				Group:              args.String("group"),
				Since:              args.Duration("since"),
				Limit:              args.Int("limit"),
				ExcludeSuppressed:  args.Bool("exclude_suppressed"),
				UnacknowledgedOnly: args.Bool("unacknowledged"),
			}
			if s := args.String("severity"); s != "" {
				n, err := service.SeverityValue(s)
				if err != nil {
					return nil, err
				}
				q.MinSeverity = n
			}
			problems, truncated, err := env.Service.ListProblems(ctx, q)
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: problems}
			res.Meta.Returned = len(problems)
			res.Meta.Limit = q.Limit
			res.Meta.Truncated = truncated
			if truncated {
				res.Meta.TruncatedReason = output.ReasonRowLimit
			}
			rows := make([][]string, 0, len(problems))
			suppressed := 0
			for _, p := range problems {
				host := "-"
				if len(p.Hosts) > 0 {
					host = p.Hosts[0].Name
				}
				state := ""
				if p.Acknowledged {
					state = "ack"
				}
				if p.Suppressed {
					suppressed++
					state = strings.TrimSpace(state + " suppressed")
				}
				rows = append(rows, []string{
					p.EventID, p.Severity, host, p.Age, state, p.Name,
					service.SuppressionSummary(p.SuppressedBy),
				})
			}
			res.Table = &output.Table{
				Headers: []string{"EVENT", "SEVERITY", "HOST", "AGE", "STATE", "PROBLEM", "SUPPRESSED BY"},
				Rows:    rows,
			}
			if suppressed > 0 {
				res.Warn("%d of these problems are suppressed and raise no alert", suppressed)
			}
			return res, nil
		},
	}
}

func problemsGet() *opspec.Operation {
	return &opspec.Operation{
		Name:    "problems.get",
		CLI:     []string{"problems", "get"},
		MCPTool: "zabbix_problem",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Show one active problem by event identifier.",
		Params: []opspec.Param{
			{Name: "event", Type: opspec.TypeString, Required: true, Positional: true,
				Description: "event identifier", Example: "757474"},
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			p, err := env.Service.GetProblem(ctx, args.String("event"))
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: p}
			res.Meta.Returned = 1
			return res, nil
		},
	}
}

func hostList() *opspec.Operation {
	return &opspec.Operation{
		Name:    "host.list",
		CLI:     []string{"host", "list"},
		MCPTool: "zabbix_hosts",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Find hosts by name fragment, pattern or group.",
		Long: "Matching is a case-insensitive substring over both the technical and the visible name, " +
			"and exact matches are listed first. Guessing exact names is the most common way a query comes back empty.",
		Params: []opspec.Param{
			{Name: "search", Type: opspec.TypeString, Positional: true,
				Description: "name fragment; * is allowed", Example: "web"},
			{Name: "group", Type: opspec.TypeString, Description: "limit to a host group"},
			{Name: "monitored", Type: opspec.TypeBool, Description: "exclude hosts whose monitoring is disabled"},
			limitParam(50),
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			hosts, truncated, err := env.Service.ListHosts(ctx, service.HostQuery{
				Search:        args.String("search"),
				Group:         args.String("group"),
				Limit:         args.Int("limit"),
				MonitoredOnly: args.Bool("monitored"),
			})
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: hosts}
			res.Meta.Returned = len(hosts)
			res.Meta.Limit = args.Int("limit")
			res.Meta.Truncated = truncated
			if truncated {
				res.Meta.TruncatedReason = output.ReasonRowLimit
			}
			rows := make([][]string, 0, len(hosts))
			for _, h := range hosts {
				rows = append(rows, []string{
					h.ID, h.Name, boolLabel(h.Monitored, "monitored", "disabled"),
					h.AgentAvailable, boolLabel(h.InMaintenance, "maintenance", ""),
					primaryAddress(h),
				})
			}
			res.Table = &output.Table{
				Headers: []string{"ID", "HOST", "MONITORING", "AGENT", "MAINTENANCE", "ADDRESS"},
				Rows:    rows,
			}
			return res, nil
		},
	}
}

func hostGet() *opspec.Operation {
	return &opspec.Operation{
		Name:    "host.get",
		CLI:     []string{"host", "get"},
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Show one host with its interfaces, tags and templates.",
		Params: []opspec.Param{
			{Name: "host", Type: opspec.TypeString, Required: true, Positional: true,
				Description: "host name, fragment or identifier"},
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			h, err := env.Service.ResolveHost(ctx, args.String("host"))
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: h}
			res.Meta.Returned = 1
			return res, nil
		},
	}
}

func hostStatus() *opspec.Operation {
	return &opspec.Operation{
		Name:    "host.status",
		CLI:     []string{"host", "status"},
		MCPTool: "zabbix_host_status",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Summarise one host: availability, active problems, data freshness, maintenance.",
		Long:    "An aggregate over several API calls, returned as a handful of fields rather than several thousand characters of configuration.",
		Params: []opspec.Param{
			{Name: "host", Type: opspec.TypeString, Required: true, Positional: true,
				Description: "host name, fragment or identifier"},
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			st, err := env.Service.StatusOf(ctx, args.String("host"))
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: st}
			res.Meta.Returned = 1
			res.Table = &output.Table{
				Headers: []string{"FIELD", "VALUE"},
				Rows: [][]string{
					{"host", st.Host},
					{"monitoring", boolLabel(st.Monitored, "enabled", "disabled")},
					{"agent", st.AgentAvailable},
					{"active problems", itoa(st.ActiveProblems)},
					{"suppressed", itoa(st.SuppressedCount)},
					{"highest severity", orDash(st.HighestSeverity)},
					{"stale or silent items", itoa(st.StaleItems)},
					{"unsupported items", itoa(st.UnsupportedItems)},
					{"maintenance", maintenanceLabel(st)},
				},
			}
			return res, nil
		},
	}
}

func hostInvestigate() *opspec.Operation {
	return &opspec.Operation{
		Name:    "host.investigate",
		CLI:     []string{"host", "investigate"},
		MCPTool: "zabbix_host_investigate",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Collect a diagnostic snapshot of one host in a single call.",
		Long: "Gathers host state, active problems, recent events, silent and unsupported items and maintenance windows. " +
			"It reports facts and draws no conclusions; interpreting them is the caller's job.",
		Params: []opspec.Param{
			{Name: "host", Type: opspec.TypeString, Required: true, Positional: true,
				Description: "host name, fragment or identifier"},
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			inv, err := env.Service.Investigate(ctx, args.String("host"))
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: inv}
			res.Meta.Returned = 1
			res.Meta.Partial = inv.Partial
			for _, w := range inv.Warnings {
				res.Warn("%s", w)
			}
			return res, nil
		},
	}
}

func metricsLatest() *opspec.Operation {
	return &opspec.Operation{
		Name:    "metrics.latest",
		CLI:     []string{"metrics", "latest"},
		MCPTool: "zabbix_metrics_latest",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Show the newest value of a host's items.",
		Long: "Values come from history, because item.lastvalue has returned a constant zero for several releases. " +
			"The history type is derived from each item automatically; querying it wrongly returns nothing rather than an error.",
		Params: []opspec.Param{
			{Name: "host", Type: opspec.TypeString, Required: true, Positional: true, Description: "host name or fragment"},
			{Name: "search", Type: opspec.TypeString, Description: "item name or key fragment", Example: "cpu"},
			limitParam(25),
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			host, values, truncated, err := env.Service.LatestValues(ctx, service.ItemQuery{
				Host:   args.String("host"),
				Search: args.String("search"),
				Limit:  args.Int("limit"),
			})
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: map[string]any{"host": host.Name, "values": values}}
			res.Meta.Returned = len(values)
			res.Meta.Limit = args.Int("limit")
			res.Meta.Truncated = truncated
			if truncated {
				res.Meta.TruncatedReason = output.ReasonRowLimit
			}
			rows := make([][]string, 0, len(values))
			silent := 0
			readErrors := 0
			for _, v := range values {
				state := ""
				switch {
				case v.ReadError != "":
					state = "read error"
					readErrors++
				case v.NoData:
					state = "no data"
					silent++
				case v.Stale:
					state = "stale"
					silent++
				}
				rows = append(rows, []string{v.Name, v.Key, v.Value, orDash(v.Age), state})
			}
			res.Table = &output.Table{
				Headers: []string{"ITEM", "KEY", "VALUE", "AGE", "STATE"},
				Rows:    rows,
			}
			if silent > 0 {
				res.Warn("%d item(s) are silent or stale", silent)
			}
			if readErrors > 0 {
				res.Meta.Partial = true
				res.Warn("history could not be read for %d item(s); see read_error", readErrors)
			}
			return res, nil
		},
	}
}

func metricsHistory() *opspec.Operation {
	minItems, maxItems := opspec.IntRange(1, 10)
	return &opspec.Operation{
		Name:    "metrics.history",
		CLI:     []string{"metrics", "history"},
		MCPTool: "zabbix_metrics_history",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Read an item's values over a time window, with min, max and average.",
		Long:    "Windows are written the way operators write them: 30m, 2h, 24h, 7d.",
		Params: []opspec.Param{
			{Name: "host", Type: opspec.TypeString, Required: true, Positional: true, Description: "host name or fragment"},
			{Name: "search", Type: opspec.TypeString, Positional: true, Description: "item name or key fragment", Example: "cpu util"},
			{Name: "last", Type: opspec.TypeDuration, Default: "1h", Description: "how far back to read", Example: "24h"},
			{Name: "items", Type: opspec.TypeInt, Default: 5, Min: minItems, Max: maxItems,
				Description: "maximum distinct items to read, 1 to 10"},
			limitParam(200),
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			host, series, err := env.Service.History(ctx, service.HistoryQuery{
				Host:   args.String("host"),
				Search: args.String("search"),
				Window: args.Duration("last"),
				Limit:  args.Int("limit"),
				Items:  args.Int("items"),
			})
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: map[string]any{"host": host.Name, "series": series}}
			points := 0
			truncated := false
			readErrors := 0
			rows := make([][]string, 0, len(series))
			for _, s := range series {
				points += len(s.Points)
				truncated = truncated || s.Truncated
				if s.ReadError != "" {
					readErrors++
				}
				summary := historySummary(s)
				rows = append(rows, []string{s.Name, s.Key, itoa(len(s.Points)), summary})
			}
			res.Meta.Returned = points
			res.Meta.Limit = args.Int("limit")
			res.Meta.Truncated = truncated
			if truncated {
				res.Meta.TruncatedReason = output.ReasonRowLimit
			}
			if readErrors > 0 {
				res.Meta.Partial = true
				res.Warn("history could not be read for %d item(s); see read_error", readErrors)
			}
			res.Table = &output.Table{
				Headers: []string{"ITEM", "KEY", "POINTS", "SUMMARY"},
				Rows:    rows,
			}
			return res, nil
		},
	}
}

func historySummary(s service.Series) string {
	if s.ReadError != "" {
		return "read error: " + s.ReadError
	}
	if s.Summary == nil {
		return "-"
	}
	return fmt.Sprintf("min %s / avg %s / max %s",
		service.FormatValue(trim(s.Summary.Min), s.Units, s.ValueType),
		service.FormatValue(trim(s.Summary.Avg), s.Units, s.ValueType),
		service.FormatValue(trim(s.Summary.Max), s.Units, s.ValueType))
}

func alertWhy() *opspec.Operation {
	return &opspec.Operation{
		Name:    "alert.why",
		CLI:     []string{"alert", "why"},
		MCPTool: "zabbix_alert_why",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Explain whether an event produced a notification, and what stood in the way.",
		Long: "Walks the whole chain: suppression, delivery attempts and their errors, trigger actions, " +
			"media types, and each recipient's media severity filter and active period. " +
			"Any one of those links can drop a notification without recording an error anywhere.",
		Params: []opspec.Param{
			{Name: "event", Type: opspec.TypeString, Required: true, Positional: true,
				Description: "event identifier", Example: "757474"},
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			exp, err := env.Service.ExplainAlert(ctx, args.String("event"))
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: exp}
			res.Meta.Returned = 1
			res.Meta.Partial = exp.Partial
			for _, w := range exp.Warnings {
				res.Warn("%s", w)
			}
			rows := make([][]string, 0, len(exp.Findings))
			for _, f := range exp.Findings {
				rows = append(rows, []string{f})
			}
			res.Table = &output.Table{Headers: []string{"FINDING"}, Rows: rows}
			return res, nil
		},
	}
}

func resolveAlert() *opspec.Operation {
	return &opspec.Operation{
		Name:    "resolve",
		CLI:     []string{"resolve"},
		MCPTool: "zabbix_resolve",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "Turn the text of a notification into event, host and trigger identifiers.",
		Long: "Accepts a notification pasted out of a chat client. Without it, an instruction such as " +
			"\"acknowledge that one\" cannot be acted on, because the identifiers only exist inside the message text.",
		Params: []opspec.Param{
			{Name: "text", Type: opspec.TypeString, Required: true, Positional: true,
				Description: "the pasted notification, or a bare event identifier"},
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			r, err := env.Service.ResolveAlertText(ctx, args.String("text"))
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: r}
			res.Meta.Returned = 1
			return res, nil
		},
	}
}

func unreachable() *opspec.Operation {
	return &opspec.Operation{
		Name:    "unreachable",
		CLI:     []string{"unreachable"},
		MCPTool: "zabbix_unreachable",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "List monitored hosts Zabbix currently cannot poll.",
		Long:    "Availability lives on the interface: host.available was removed in 5.4, and code still reading it sees nothing at all.",
		Params:  []opspec.Param{limitParam(50)},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			hosts, truncated, err := env.Service.ListUnreachable(ctx, args.Int("limit"))
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: hosts}
			res.Meta.Returned = len(hosts)
			res.Meta.Limit = args.Int("limit")
			res.Meta.Truncated = truncated
			if truncated {
				res.Meta.TruncatedReason = output.ReasonRowLimit
			}
			rows := make([][]string, 0, len(hosts))
			for _, h := range hosts {
				kind, addr, msg := "-", "-", ""
				if len(h.Interfaces) > 0 {
					kind = h.Interfaces[0].Type
					addr = h.Interfaces[0].Address
					msg = h.Interfaces[0].Error
				}
				rows = append(rows, []string{
					h.Host, kind, addr, boolLabel(h.InMaintenance, "maintenance", ""), msg,
				})
			}
			res.Table = &output.Table{
				Headers: []string{"HOST", "INTERFACE", "ADDRESS", "MAINTENANCE", "ERROR"},
				Rows:    rows,
			}
			return res, nil
		},
	}
}

func triggersList() *opspec.Operation {
	return &opspec.Operation{
		Name:    "triggers.list",
		CLI:     []string{"triggers", "list"},
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "List triggers, optionally only those currently firing.",
		Params: []opspec.Param{
			{Name: "host", Type: opspec.TypeString, Description: "limit to one host"},
			{Name: "search", Type: opspec.TypeString, Description: "description fragment"},
			{Name: "problems", Type: opspec.TypeBool, Description: "only triggers currently in a problem state"},
			limitParam(50),
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			triggers, truncated, err := env.Service.ListTriggers(ctx, service.TriggerQuery{
				Host:         args.String("host"),
				Search:       args.String("search"),
				ProblemsOnly: args.Bool("problems"),
				Limit:        args.Int("limit"),
			})
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: triggers}
			res.Meta.Returned = len(triggers)
			res.Meta.Limit = args.Int("limit")
			res.Meta.Truncated = truncated
			if truncated {
				res.Meta.TruncatedReason = output.ReasonRowLimit
			}
			rows := make([][]string, 0, len(triggers))
			for _, t := range triggers {
				host := "-"
				if len(t.Hosts) > 0 {
					host = t.Hosts[0].Name
				}
				rows = append(rows, []string{
					t.ID, host, t.Severity, boolLabel(t.Enabled, "enabled", "disabled"), t.Value, t.Description,
				})
			}
			res.Table = &output.Table{
				Headers: []string{"ID", "HOST", "SEVERITY", "STATUS", "STATE", "DESCRIPTION"},
				Rows:    rows,
			}
			return res, nil
		},
	}
}

func maintenanceList() *opspec.Operation {
	return &opspec.Operation{
		Name:    "maintenance.list",
		CLI:     []string{"maintenance", "list"},
		MCPTool: "zabbix_maintenance_list",
		Risk:    safety.RiskRead,
		Scope:   safety.ScopeRead,
		Summary: "List maintenance windows, including expired ones.",
		Long: "Expired windows are shown by default: removing one that has already lapsed is a routine follow-up, " +
			"and it cannot be done if the window is invisible.",
		Params: []opspec.Param{
			{Name: "host", Type: opspec.TypeString, Description: "only windows covering this host"},
			{Name: "search", Type: opspec.TypeString, Description: "name fragment"},
			{Name: "active", Type: opspec.TypeBool, Description: "hide windows whose period has passed"},
			limitParam(50),
		},
		Run: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*output.Result, error) {
			q := service.MaintenanceQuery{
				Search:         args.String("search"),
				Limit:          args.Int("limit"),
				ExcludeExpired: args.Bool("active"),
			}
			if h := args.String("host"); h != "" {
				host, err := env.Service.ResolveHost(ctx, h)
				if err != nil {
					return nil, err
				}
				q.HostID = host.ID
			}
			windows, truncated, err := env.Service.ListMaintenance(ctx, q)
			if err != nil {
				return nil, err
			}
			res := &output.Result{Data: windows}
			res.Meta.Returned = len(windows)
			res.Meta.Limit = args.Int("limit")
			res.Meta.Truncated = truncated
			if truncated {
				res.Meta.TruncatedReason = output.ReasonRowLimit
			}
			rows := make([][]string, 0, len(windows))
			expired := 0
			for _, m := range windows {
				state := "scheduled"
				switch {
				case m.Expired:
					state = "expired"
					expired++
				case m.Active:
					state = "active"
				}
				rows = append(rows, []string{
					m.ID, m.Name, state, m.ActiveTill, orDash(m.EndsIn),
					itoa(len(m.Hosts)), boolLabel(m.Collecting, "collecting", "no data"),
				})
			}
			res.Table = &output.Table{
				Headers: []string{"ID", "NAME", "STATE", "ENDS", "IN", "HOSTS", "DATA"},
				Rows:    rows,
			}
			if expired > 0 {
				res.Warn("%d window(s) have already expired and can be deleted", expired)
			}
			return res, nil
		},
	}
}

func boolLabel(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func primaryAddress(h service.Host) string {
	for _, i := range h.Interfaces {
		if i.Main {
			return i.Address
		}
	}
	if len(h.Interfaces) > 0 {
		return h.Interfaces[0].Address
	}
	return "-"
}

func maintenanceLabel(st *service.HostStatus) string {
	if !st.InMaintenance {
		return "none"
	}
	if st.MaintenanceName != "" {
		return st.MaintenanceName
	}
	return "in maintenance"
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func trim(f float64) string { return strings.TrimSuffix(fmt.Sprintf("%.4f", f), ".0000") }
