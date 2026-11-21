package segment

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Describe turns a rule into plain sentences, one per condition, e.g.
// "Last purchase: not in the last 60 days (or never)". The message writer gets
// these, so the copy can speak to what the audience has in common.
func Describe(r Rule) []string {
	var out []string
	for _, c := range r.Conditions {
		if !c.isGroup() {
			out = append(out, describeLeaf(c))
			continue
		}
		parts := make([]string, len(c.Conditions))
		for i, inner := range c.Conditions {
			parts[i] = describeLeaf(inner)
		}
		join := " and "
		if c.Match == MatchAny {
			join = " or "
		}
		out = append(out, "("+strings.Join(parts, join)+")")
	}
	if r.Match == MatchAny && len(out) > 1 {
		return []string{"Any of: " + strings.Join(out, "; ")}
	}
	return out
}

func describeLeaf(c Condition) string {
	f, ok := fieldByName(c.Field)
	label := c.Field
	if ok {
		label = f.Label
	}
	var v any
	_ = json.Unmarshal(c.Value, &v)
	switch c.Op {
	case OpEq:
		return fmt.Sprintf("%s: %v", label, v)
	case OpNeq:
		return fmt.Sprintf("%s: not %v", label, v)
	case OpIn:
		return fmt.Sprintf("%s: %s", label, joinValues(v, " or "))
	case OpNotIn:
		return fmt.Sprintf("%s: not %s", label, joinValues(v, " or "))
	case OpGt:
		return fmt.Sprintf("%s: more than %v", label, v)
	case OpGte:
		return fmt.Sprintf("%s: at least %v", label, v)
	case OpLt:
		return fmt.Sprintf("%s: less than %v", label, v)
	case OpLte:
		return fmt.Sprintf("%s: at most %v", label, v)
	case OpBetween:
		return fmt.Sprintf("%s: between %s", label, joinValues(v, " and "))
	case OpWithinDays:
		return fmt.Sprintf("%s: in the last %v days", label, v)
	case OpNotWithinDays:
		return fmt.Sprintf("%s: not in the last %v days (or never)", label, v)
	case OpEver:
		return label + ": at some point"
	case OpNever:
		return label + ": never"
	}
	return label
}

func joinValues(v any, sep string) string {
	list, _ := v.([]any)
	parts := make([]string, len(list))
	for i, x := range list {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, sep)
}
