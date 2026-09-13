package safety

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Classification is the verdict of the risk registry for one raw API method.
type Classification struct {
	Risk  Risk
	Scope string
	// Allowed is false for a method this program refuses to call at all.
	Allowed bool
	// Reason explains a refusal.
	Reason string
}

// Scope names, matching the values a profile may grant.
const (
	ScopeRead          = "read"
	ScopeAcknowledge   = "acknowledge"
	ScopeMaintenance   = "maintenance"
	ScopeConfiguration = "configuration"
)

// deniedMethods are refused regardless of profile scope.
//
// Each entry either hands out a credential, executes code, or moves data that
// carries credentials. None of them belongs in a path an agent can reach, and
// none of them is worth the risk of a heuristic getting it wrong.
var deniedMethods = map[string]string{
	"user.login":            "this program authenticates with an API token; a session login would create a second credential",
	"user.logout":           "session logins are not used",
	"token.create":          "creating API tokens from here would let an agent mint its own credentials",
	"token.update":          "modifying API tokens would let an agent widen its own access",
	"token.generate":        "generating API token values would expose a credential",
	"token.delete":          "deleting API tokens can lock out other integrations",
	"script.execute":        "this runs arbitrary commands on monitored hosts",
	"task.create":           "tasks execute checks and scripts on demand",
	"configuration.export":  "exports embed macros and credentials in plain text",
	"configuration.import":  "imports rewrite templates and hosts wholesale",
	"authentication.update": "this changes how every user signs in",
	"settings.update":       "this changes installation-wide behaviour",
	"user.create":           "user administration is outside this tool's remit",
	"user.update":           "user administration is outside this tool's remit",
	"user.delete":           "user administration is outside this tool's remit",
	"usergroup.create":      "user administration is outside this tool's remit",
	"usergroup.update":      "user administration is outside this tool's remit",
	"usergroup.delete":      "user administration is outside this tool's remit",
	"role.create":           "role administration is outside this tool's remit",
	"role.update":           "role administration is outside this tool's remit",
	"role.delete":           "role administration is outside this tool's remit",
	"user.unblock":          "user administration is outside this tool's remit",
	"user.provision":        "user administration is outside this tool's remit",
	"user.resettotp":        "user administration is outside this tool's remit",
	"userdirectory.create":  "directory administration is outside this tool's remit",
	"userdirectory.update":  "directory administration is outside this tool's remit",
	"userdirectory.delete":  "directory administration is outside this tool's remit",
	"mfa.create":            "authentication administration is outside this tool's remit",
	"mfa.update":            "authentication administration is outside this tool's remit",
	"mfa.delete":            "authentication administration is outside this tool's remit",
	"history.clear":         "this permanently deletes collected measurements, and nothing in a diagnostic workflow needs to",
	"usermacro.get":         "macro values are where installations keep database passwords and API keys, and this tool's output goes into a model's context",
}

// readOnlyObjects are the API objects whose .get method this program will call
// through the raw escape hatch.
var readOnlyObjects = []string{
	"action", "alert", "api", "auditlog", "autoregistration", "configuration",
	"connector", "correlation", "dashboard", "dhost", "discoveryrule", "drule",
	"dservice", "event", "graph", "graphitem", "graphprototype", "hanode",
	"history", "host", "hostgroup", "hostinterface", "hostprototype", "housekeeping",
	"httptest", "iconmap", "image", "item", "itemprototype", "maintenance", "map",
	"mediatype", "problem", "proxy", "proxygroup", "regexp", "report", "role",
	"script", "service", "sla", "task", "template", "templatedashboard",
	"templategroup", "token", "trend", "trigger", "triggerprototype", "user",
	"userdirectory", "usergroup", "usermacro", "valuemap", "webscenario",
}

// writeObjects maps an object to the scope its writes require. An object
// absent from this map cannot be written through the escape hatch.
var writeObjects = map[string]string{
	"maintenance":      ScopeMaintenance,
	"event":            ScopeAcknowledge,
	"host":             ScopeConfiguration,
	"hostgroup":        ScopeConfiguration,
	"hostinterface":    ScopeConfiguration,
	"item":             ScopeConfiguration,
	"trigger":          ScopeConfiguration,
	"triggerprototype": ScopeConfiguration,
	"template":         ScopeConfiguration,
	"templategroup":    ScopeConfiguration,
	"usermacro":        ScopeConfiguration,
	"valuemap":         ScopeConfiguration,
	"service":          ScopeConfiguration,
	"sla":              ScopeConfiguration,
	"dashboard":        ScopeConfiguration,
	"correlation":      ScopeConfiguration,
	"graph":            ScopeConfiguration,
	"graphprototype":   ScopeConfiguration,
	"regexp":           ScopeConfiguration,
	"iconmap":          ScopeConfiguration,
	"image":            ScopeConfiguration,
	"map":              ScopeConfiguration,
	"report":           ScopeConfiguration,
	"housekeeping":     ScopeConfiguration,
}

// executableObjects are objects whose configuration is code, or invokes code.
//
// Denying script.execute closes the short road to running a command on a
// monitored host. These are the long ones: a script plus an action that runs
// it, an item of type SSH, Telnet, script or browser, a script media type that
// fires on every alert, a connector that streams the installation's data to an
// endpoint of the author's choosing. Each is a legitimate part of Zabbix and
// none of it belongs behind a generic escape hatch, so they are refused
// outright rather than left behind a scope a profile might hold.
var executableObjects = map[string]string{
	"script":    "a script is a command definition, and an action can run it without script.execute ever being called",
	"action":    "action operations run scripts and remote commands on hosts",
	"mediatype": "a script media type executes a program on the Zabbix server for every alert it sends",
	// "item" is deliberately absent: an item is refused by its type, not by
	// its method name — see executableItemTypes and ClassifyCall. Blanket
	// refusal also cost the ordinary case, and building a monitoring item is
	// the most common reason to reach for the escape hatch at all.
	"itemprototype":    "item prototypes become items, and items can execute code, and a prototype's type cannot be checked against the items discovery will create",
	"discoveryrule":    "a discovery rule creates items from its prototypes",
	"hostprototype":    "host prototypes carry the items discovery creates",
	"httptest":         "a web scenario makes the Zabbix server issue requests of the author's choosing",
	"webscenario":      "a web scenario makes the Zabbix server issue requests of the author's choosing",
	"connector":        "a connector streams monitoring data to an external endpoint",
	"autoregistration": "autoregistration decides what happens to every new host that appears",
	"proxy":            "a proxy collects for the hosts assigned to it, and its address decides where they report",
	"proxygroup":       "proxy groups decide which proxy collects for which hosts",
}

// executableItemTypes are the item `type` values whose collection runs code, or
// makes the server fetch something of the author's choosing. The numbers are
// Zabbix's own, from the `type` field of the item object.
//
// Everything else — agent, agent (active), trapper, internal, simple check,
// SNMP, calculated, dependent, JMX, IPMI — reads a value that something else
// already produces, and is the whole point of letting items be written here.
var executableItemTypes = map[int]string{
	10: "an external check item runs a script on the Zabbix server for every collection",
	11: "a database monitor item runs SQL of the author's choosing",
	13: "an SSH agent item runs a command on the monitored host",
	14: "a Telnet agent item runs a command on the monitored host",
	19: "an HTTP agent item makes the Zabbix server issue requests of the author's choosing",
	20: "a script item runs JavaScript on the Zabbix server",
	21: "a browser item drives a headless browser from the Zabbix server",
}

// ClassifyCall is ClassifyMethod plus what the call actually carries.
//
// The method name alone decides nothing for items: `item.create` is how a host
// gets an ordinary agent check, and also how a script item that runs JavaScript
// on the server appears. The `type` field is what separates them, so the gate
// reads it — and refuses when it is absent, because a type nobody named cannot
// be shown to be harmless. Callers that have no params (listing the accepted
// methods, for instance) stay on ClassifyMethod.
func ClassifyCall(method string, params any) Classification {
	class := ClassifyMethod(method)
	if !class.Allowed {
		return class
	}
	object, action, ok := strings.Cut(strings.ToLower(strings.TrimSpace(method)), ".")
	if !ok || object != "item" || action == "get" {
		return class
	}
	// Deleting an item carries no type and executes nothing; it stays a
	// destructive write like any other.
	if destructiveActions[action] {
		return class
	}
	if reason := refuseItemWrite(action, params); reason != "" {
		return Classification{Allowed: false, Reason: reason}
	}
	return class
}

// refuseItemWrite returns the reason an item write is refused, or "" to allow it.
func refuseItemWrite(action string, params any) string {
	// item.copy duplicates items this call never names: their types live in the
	// installation, not in these params, so one script item copied to twenty
	// hosts would be twenty new executions this gate never saw.
	if action == "copy" {
		return "item.copy duplicates items whose type this call cannot see, and some item types execute code"
	}
	objects, ok := itemObjects(params)
	if !ok {
		return "an item write must pass an object, or a list of objects, so that each item's type can be read"
	}
	for _, obj := range objects {
		raw, present := obj["type"]
		if !present {
			// On update Zabbix keeps the stored type, and for an SSH, script or
			// browser item the `params` field IS its code: editing it without
			// naming the type would be editing code sight unseen. On create an
			// omitted type is not a default worth guessing either.
			return "an item write must state `type` explicitly: without it this call could create or edit an item whose type executes code (item.get shows the current type)"
		}
		itemType, ok := asItemType(raw)
		if !ok {
			return "item `type` must be a number, the way Zabbix defines it"
		}
		if reason, executable := executableItemTypes[itemType]; executable {
			return fmt.Sprintf("item type %d is refused: %s", itemType, reason)
		}
	}
	return ""
}

// itemObjects normalises the two shapes Zabbix accepts — one object or an array
// of them — into a slice. Anything else is not an item write.
func itemObjects(params any) ([]map[string]any, bool) {
	switch v := params.(type) {
	case map[string]any:
		return []map[string]any{v}, true
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, e := range v {
			obj, ok := e.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, obj)
		}
		return out, len(out) > 0
	}
	return nil, false
}

// asItemType reads Zabbix's `type` field, which arrives as a JSON number from
// this program and as a string from most of Zabbix's own output.
func asItemType(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), v == float64(int(v))
	case int:
		return v, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	}
	return 0, false
}

var writeActions = map[string]bool{
	"create": true, "update": true, "massadd": true, "massupdate": true,
	"massremove": true, "replacehostinterfaces": true, "copy": true,
	"acknowledge": true, "createglobal": true, "updateglobal": true,
	"adddependencies": true, "deletedependencies": true, "propertyupdate": true,
}

var destructiveActions = map[string]bool{
	"delete": true, "deleteglobal": true, "clear": true,
}

// alwaysRead are exact methods that read despite not ending in ".get".
var alwaysRead = map[string]bool{
	"apiinfo.version":             true,
	"user.checkauthentication":    false,
	"script.getscriptsbyhosts":    true,
	"script.getscriptsbyevents":   true,
	"configuration.importcompare": false,
}

// ClassifyMethod decides what a raw Zabbix API method may do.
//
// The table is explicit on purpose. Classifying by suffix alone would call
// script.execute an ordinary read and task.create an ordinary write, and an
// unrecognised method would be waved through on the strength of its name.
// Anything not listed here is refused.
func ClassifyMethod(method string) Classification {
	m := strings.ToLower(strings.TrimSpace(method))
	if reason, denied := deniedMethods[m]; denied {
		return Classification{Allowed: false, Reason: reason}
	}
	if allowed, ok := alwaysRead[m]; ok {
		if !allowed {
			return Classification{Allowed: false, Reason: "this method is not part of the supported surface"}
		}
		return Classification{Risk: RiskRead, Scope: ScopeRead, Allowed: true}
	}
	object, action, ok := strings.Cut(m, ".")
	if !ok || object == "" || action == "" {
		return Classification{Allowed: false,
			Reason: "a Zabbix API method looks like object.action, for example host.get"}
	}
	if reason, executable := executableObjects[object]; executable && action != "get" {
		return Classification{Allowed: false, Reason: reason}
	}
	switch {
	case action == "get":
		if contains(readOnlyObjects, object) {
			return Classification{Risk: RiskRead, Scope: ScopeRead, Allowed: true}
		}
	case destructiveActions[action]:
		if scope, ok := writeObjects[object]; ok {
			return Classification{Risk: RiskDestructive, Scope: scope, Allowed: true}
		}
	case writeActions[action]:
		if scope, ok := writeObjects[object]; ok {
			return Classification{Risk: RiskWrite, Scope: scope, Allowed: true}
		}
	}
	return Classification{Allowed: false,
		Reason: "this method is not in the risk registry, and an unclassified method is refused rather than guessed at"}
}

// KnownMethods lists every method the escape hatch accepts, for the schema
// command and for error messages that would otherwise leave a caller guessing.
func KnownMethods() []string {
	set := map[string]bool{"apiinfo.version": true, "script.getscriptsbyhosts": true, "script.getscriptsbyevents": true}
	for _, o := range readOnlyObjects {
		set[o+".get"] = true
	}
	for o := range writeObjects {
		for a := range writeActions {
			set[o+"."+a] = true
		}
		for a := range destructiveActions {
			set[o+"."+a] = true
		}
	}
	for m := range deniedMethods {
		delete(set, m)
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
