package safety

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// LimitedUpdate identifies the only field-limited exceptions to executable
// object writes. Method-only classification deliberately continues to refuse them.
func LimitedUpdate(method string) bool {
	m := strings.ToLower(strings.TrimSpace(method))
	return m == "mediatype.update" || m == "discoveryrule.update"
}

func classifyLimitedUpdate(method string, params any) Classification {
	objects, ok := itemObjects(params)
	deny := func(reason string) Classification { return Classification{Reason: reason} }
	if !ok {
		return deny("a constrained update requires a non-empty object or list of objects")
	}
	media := strings.EqualFold(strings.TrimSpace(method), "mediatype.update")
	idField := "itemid"
	fields := map[string]bool{"lifetime_type": true, "lifetime": true, "enabled_lifetime_type": true, "enabled_lifetime": true}
	if media {
		idField = "mediatypeid"
		fields = map[string]bool{"status": true}
	}
	risk := RiskWrite
	seen := map[string]bool{}
	for _, obj := range objects {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k != idField && !fields[k] {
				return deny(fmt.Sprintf("field %q is refused for %s; only %s and %s are permitted", k, method, idField, allowedFieldNames(fields)))
			}
		}
		id, ok := obj[idField].(string)
		n, err := strconv.ParseUint(id, 10, 64)
		if !ok || err != nil || n == 0 {
			return deny(fmt.Sprintf("%s must be a non-zero numeric ID string", idField))
		}
		if seen[id] {
			return deny(fmt.Sprintf("duplicate %s %q in one update", idField, id))
		}
		seen[id] = true
		if len(obj) < 2 {
			if media {
				return deny("status is required")
			}
			return deny("at least one permitted change is required")
		}
		for _, k := range keys {
			if k == idField {
				continue
			}
			if k == "status" || strings.HasSuffix(k, "_type") {
				v, ok := asItemType(obj[k])
				max := 2
				if media {
					max = 1
				}
				if !ok || v < 0 || v > max {
					return deny(fmt.Sprintf("%s must be an integer from 0 to %d", k, max))
				}
				if k == "status" && v == 1 || k == "lifetime_type" && v != 1 {
					risk = RiskDestructive
				}
			} else {
				v, ok := obj[k].(string)
				if !ok || strings.TrimSpace(v) == "" {
					return deny(fmt.Sprintf("%s must be a non-empty Zabbix lifetime string", k))
				}
			}
		}
		// A duration-only delete-policy edit may shorten an existing deletion
		// deadline. Without the policy mode, classify conservatively.
		if _, present := obj["lifetime"]; present {
			if _, mode := obj["lifetime_type"]; !mode {
				risk = RiskDestructive
			}
		}
	}
	return Classification{Allowed: true, Risk: risk, Scope: ScopeConfiguration}
}

func allowedFieldNames(fields map[string]bool) string {
	names := make([]string, 0, len(fields))
	for f := range fields {
		names = append(names, f)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
