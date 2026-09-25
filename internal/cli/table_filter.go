package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Filters for `creght table record list`.
//
// The platform used to drop a filter it did not understand — {"field":..,"op":..},
// {"and":[..]} — and return the whole table, so a caller saw rows and believed
// they were filtered. The CLI now checks the filter before sending it and fails
// with the correct shape instead. The same checks run on the platform; doing
// them here too keeps the error the same against an older backend.

const tableFilterExample = `{"conditions":[{"fieldId":"channel_id","operator":"eq","value":"abc"},{"fieldId":"views","operator":"gte","value":100}]}`

const tableFilterOperatorList = "eq, neq, in, gt, gte, lt, lte, between"

// tableFilterOperators maps every operator the platform accepts to its
// canonical name.
var tableFilterOperators = map[string]string{
	"eq": "eq", "=": "eq", "equal": "eq",
	"neq": "neq", "!=": "neq", "not_equal": "neq", "notEqual": "neq",
	"in": "in",
	"gt": "gt", ">": "gt",
	"gte": "gte", ">=": "gte",
	"lt": "lt", "<": "lt",
	"lte": "lte", "<=": "lte",
	"between": "between",
}

// tableFilterKeyHints names the key a common mistake meant.
var tableFilterKeyHints = map[string]string{
	"field": "fieldId", "field_name": "fieldId", "key": "fieldId", "name": "fieldId",
	"op": "operator", "operation": "operator", "cmp": "operator",
	"val": "value",
	"and": "conditions", "where": "conditions", "filters": "conditions", "or": "conditions",
}

func tableFilterError(format string, args ...any) error {
	return fmt.Errorf("invalid --filter: "+format+"\n  operators: %s (in takes an array, between takes [from, to], both inclusive)\n  example:   --filter='%s'",
		append(args, tableFilterOperatorList, tableFilterExample)...)
}

func describeUnknownKeys(raw map[string]any, allowed ...string) []string {
	allowedSet := map[string]bool{}
	for _, key := range allowed {
		allowedSet[key] = true
	}
	var out []string
	for key := range raw {
		if allowedSet[key] {
			continue
		}
		if hint, ok := tableFilterKeyHints[key]; ok {
			out = append(out, fmt.Sprintf("%q (did you mean %q?)", key, hint))
		} else {
			out = append(out, fmt.Sprintf("%q", key))
		}
	}
	sort.Strings(out)
	return out
}

// normalizeTableFieldID accepts both spellings of a business field: the bare
// key and the body.<key> form --order_by uses.
func normalizeTableFieldID(field string) string {
	return strings.TrimPrefix(strings.TrimSpace(field), "body.")
}

// readJSONObjectArg reads a flag that takes a JSON object either inline or as
// a file path: a value starting with "{" is the JSON itself.
func readJSONObjectArg(flagName string, value string) (map[string]any, error) {
	value = strings.TrimSpace(value)
	var body []byte
	source := "--" + flagName
	if strings.HasPrefix(value, "{") {
		body = []byte(value)
	} else {
		var err error
		body, err = os.ReadFile(value)
		if err != nil {
			return nil, fmt.Errorf("read --%s file %s: %w (pass inline JSON starting with { or a JSON file path)", flagName, value, err)
		}
		source = value
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w (expected a JSON object)", source, err)
	}
	return out, nil
}

// validateTableRecordFilter checks a --filter object and rewrites it into the
// exact shape the platform binds: fieldId without body., canonical operator,
// the value under "value".
func validateTableRecordFilter(filter map[string]any) (map[string]any, error) {
	if unknown := describeUnknownKeys(filter, "conditions", "match"); len(unknown) > 0 {
		return nil, tableFilterError("unknown key %s; a filter is {\"conditions\":[...]} and every condition must hold (AND)", strings.Join(unknown, ", "))
	}
	if match, ok := filter["match"]; ok {
		if s, _ := match.(string); s != "" && s != "and" {
			return nil, tableFilterError("match %v is not supported; conditions are always AND-ed (for OR, run one query per branch and merge)", match)
		}
	}
	rawConditions, ok := filter["conditions"]
	if !ok {
		return nil, tableFilterError("missing \"conditions\"")
	}
	conditions, ok := rawConditions.([]any)
	if !ok {
		return nil, tableFilterError("\"conditions\" must be an array")
	}

	out := make([]any, 0, len(conditions))
	for i, item := range conditions {
		n := i + 1
		condition, ok := item.(map[string]any)
		if !ok {
			return nil, tableFilterError("condition #%d must be an object", n)
		}
		if unknown := describeUnknownKeys(condition, "fieldId", "field_id", "operator", "value", "values"); len(unknown) > 0 {
			return nil, tableFilterError("condition #%d has unknown key %s", n, strings.Join(unknown, ", "))
		}

		field, _ := condition["fieldId"].(string)
		if field == "" {
			field, _ = condition["field_id"].(string)
		}
		field = normalizeTableFieldID(field)
		if field == "" {
			return nil, tableFilterError("condition #%d has no \"fieldId\" (the record field, e.g. \"channel_id\" or \"body.channel_id\")", n)
		}

		rawOp, _ := condition["operator"].(string)
		if strings.TrimSpace(rawOp) == "" {
			return nil, tableFilterError("condition #%d on %q has no \"operator\"", n, field)
		}
		op, ok := tableFilterOperators[strings.TrimSpace(rawOp)]
		if !ok {
			return nil, tableFilterError("condition #%d on %q has unsupported operator %q", n, field, rawOp)
		}

		value, hasValue := condition["value"]
		if !hasValue {
			value, hasValue = condition["values"]
		}
		if !hasValue {
			return nil, tableFilterError("condition #%d on %q has no \"value\"", n, field)
		}
		switch op {
		case "in":
			if _, ok := value.([]any); !ok {
				return nil, tableFilterError("condition #%d: operator \"in\" on %q needs an array value", n, field)
			}
		case "between":
			if values, ok := value.([]any); !ok || len(values) != 2 {
				return nil, tableFilterError("condition #%d: operator \"between\" on %q needs value [from, to]", n, field)
			}
		}

		out = append(out, map[string]any{"fieldId": field, "operator": op, "value": value})
	}
	return map[string]any{"conditions": out}, nil
}

// normalizeTableRecordWhere strips body. from --where keys, the equality
// shorthand {"<field>": <value>, ...}.
func normalizeTableRecordWhere(where map[string]any) (map[string]any, error) {
	if _, ok := where["conditions"]; ok {
		return nil, fmt.Errorf("invalid --where: it is the equality shorthand {\"<field>\": <value>, ...}; a {\"conditions\":[...]} object goes in --filter")
	}
	out := make(map[string]any, len(where))
	for key, value := range where {
		field := normalizeTableFieldID(key)
		if field == "" {
			return nil, fmt.Errorf("invalid --where: empty field name; --where is {\"<field>\": <value>, ...}, e.g. --where='{\"channel_id\":\"abc\"}'")
		}
		out[field] = value
	}
	return out, nil
}
