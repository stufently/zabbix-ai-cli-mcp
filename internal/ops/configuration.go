package ops

import (
	"context"
	"strconv"
	"strings"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/opspec"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/safety"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/service"
)

func init() {
	register(mediaStateOperation(true), mediaStateOperation(false), lostResourcesOperation())
}

func mediaStateOperation(enable bool) *opspec.Operation {
	name := "disable"
	summary := "Disable a media type by mediatypeid or exact name; change only status and show affected users/actions."
	risk := safety.RiskDestructive
	if enable {
		name = "enable"
		risk = safety.RiskWrite
		summary = "Enable a media type by mediatypeid or exact name; change only status, including Script/Webhook."
	}
	return &opspec.Operation{Name: "mediatypes." + name, CLI: []string{"mediatypes", name}, Summary: summary,
		Long: "Idempotent: requesting the current state succeeds. Safe to repeat from an external cron; no in-process timer is installed.",
		Risk: risk, Scope: safety.ScopeConfiguration,
		Params: []opspec.Param{
			{Name: "mediatypeid", Type: opspec.TypeString, Description: "media type ID; supply this or name"},
			{Name: "name", Type: opspec.TypeString, Description: "exact media type name; supply this or mediatypeid"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			return env.Service.PlanMediaTypeState(ctx, env.Profile, args.String("mediatypeid"), args.String("name"), enable)
		},
	}
}

func lostResourcesOperation() *opspec.Operation {
	minMode, maxMode := opspec.IntRange(0, 2)
	return &opspec.Operation{Name: "discoveryrules.set_lost_resources", CLI: []string{"discoveryrules", "set-lost-resources"},
		Summary: "Set lost LLD resource policy by itemid or template + rule name; only lifetime_type/lifetime/enabled_lifetime_type/enabled_lifetime. Shows host and lost-item counts; deletion is destructive.",
		Risk:    safety.RiskWrite, Scope: safety.ScopeConfiguration,
		Params: []opspec.Param{
			{Name: "itemid", Type: opspec.TypeString, Description: "LLD rule ID; use this or template + name"},
			{Name: "template", Type: opspec.TypeString, Description: "exact template visible name; requires name instead of itemid"},
			{Name: "name", Type: opspec.TypeString, Description: "exact LLD rule name within template"},
			{Name: "lifetime_type", Type: opspec.TypeInt, Min: minMode, Max: maxMode, Description: "delete lost resources: 0 after lifetime, 1 never, 2 immediately (history lost)"},
			{Name: "lifetime", Type: opspec.TypeString, Description: "Zabbix delete lifetime, e.g. 7d, seconds or user macro"},
			{Name: "enabled_lifetime_type", Type: opspec.TypeInt, Min: minMode, Max: maxMode, Description: "disable lost resources: 0 after enabled_lifetime, 1 never, 2 immediately"},
			{Name: "enabled_lifetime", Type: opspec.TypeString, Description: "Zabbix disable lifetime, e.g. 1h, seconds or user macro"},
		},
		Plan: func(ctx context.Context, env *opspec.Env, args *opspec.Args) (*safety.Plan, error) {
			changes := map[string]any{}
			for _, f := range service.LostResourceFields {
				if args.Has(f) {
					if strings.HasSuffix(f, "_type") {
						changes[f] = strconv.Itoa(args.Int(f))
					} else {
						changes[f] = args.String(f)
					}
				}
			}
			return env.Service.PlanLostResources(ctx, env.Profile, args.String("itemid"), args.String("template"), args.String("name"), changes)
		},
	}
}
