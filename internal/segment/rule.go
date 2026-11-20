package segment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"campaign-composer/internal/schema"
)

type Match string

const (
	MatchAll Match = "all"
	MatchAny Match = "any"
)

// Rule is an audience definition: conditions joined by AND (all) or OR (any).
type Rule struct {
	Match      Match       `json:"match"`
	Conditions []Condition `json:"conditions"`
}

// Condition is either a leaf (Field, Op, Value) or a group (Match, Conditions).
// Groups can only contain leaves, so rules are at most two levels deep: enough
// for "A and (B or C)", and a fixed depth is something Claude's structured
// outputs can describe (they do not allow recursive schemas).
type Condition struct {
	Field string          `json:"field,omitempty"`
	Op    Op              `json:"op,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`

	Match      Match       `json:"match,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

func (c Condition) isGroup() bool { return c.Match != "" || c.Conditions != nil }

// Schema is the JSON schema of a rule. The same schema is sent to Claude as the
// structured-output format and used here to check rules from any source. Field
// and operator names come from the catalog, so the two can never drift apart.
func Schema() map[string]any {
	value := map[string]any{"anyOf": []any{
		map[string]any{"type": "string"},
		map[string]any{"type": "number"},
		map[string]any{"type": "array", "items": map[string]any{"anyOf": []any{
			map[string]any{"type": "string"}, map[string]any{"type": "number"},
		}}},
		map[string]any{"type": "null"},
	}}
	match := map[string]any{"type": "string", "enum": []string{string(MatchAll), string(MatchAny)}}
	leaf := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"field", "op", "value"},
		"properties": map[string]any{
			"field": map[string]any{"type": "string", "enum": fieldNames()},
			"op":    map[string]any{"type": "string", "enum": allOps()},
			"value": value,
		},
	}
	group := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"match", "conditions"},
		"properties": map[string]any{
			"match":      match,
			"conditions": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/condition"}},
		},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"match", "conditions"},
		"properties": map[string]any{
			"match": match,
			"conditions": map[string]any{"type": "array", "items": map[string]any{"anyOf": []any{
				map[string]any{"$ref": "#/$defs/condition"}, map[string]any{"$ref": "#/$defs/group"},
			}}},
		},
		"$defs": map[string]any{"condition": leaf, "group": group},
	}
}

var compiledSchema = func() *schema.Schema {
	raw, err := json.Marshal(Schema())
	if err != nil {
		panic(err)
	}
	return schema.MustCompile(raw)
}()

// ValidationError lists everything wrong with a rule. The messages name the
// path and the allowed values, so they can go back to Claude for a fix or be
// shown to the marketer.
type ValidationError struct {
	Problems []string `json:"problems"`
}

func (e *ValidationError) Error() string {
	return "invalid rule: " + strings.Join(e.Problems, "; ")
}

// Parse checks raw JSON against the schema, then validates and normalizes it
// (e.g. "istanbul" becomes "İstanbul"). The returned rule is safe to compile.
func Parse(raw []byte) (Rule, error) {
	if _, err := compiledSchema.Parse(string(raw)); err != nil {
		return Rule{}, &ValidationError{Problems: []string{"rule " + err.Error()}}
	}
	var r Rule
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Rule{}, &ValidationError{Problems: []string{"rule is not valid JSON: " + err.Error()}}
	}
	return r, r.validate()
}

// validate checks the rule against the catalog and normalizes values in place.
func (r *Rule) validate() error {
	var problems []string
	add := func(path, format string, args ...any) {
		problems = append(problems, path+": "+fmt.Sprintf(format, args...))
	}
	checkMatch := func(path string, m Match) {
		if m != MatchAll && m != MatchAny {
			add(path, `match must be "all" or "any", got %q`, m)
		}
	}

	checkMatch("match", r.Match)
	if len(r.Conditions) > MaxConditions {
		add("conditions", "at most %d conditions, got %d", MaxConditions, len(r.Conditions))
	}
	for i := range r.Conditions {
		c := &r.Conditions[i]
		path := fmt.Sprintf("conditions[%d]", i)
		if !c.isGroup() {
			validateLeaf(path, c, add)
			continue
		}
		if c.Field != "" || c.Op != "" || c.Value != nil {
			add(path, "a group has only match and conditions")
		}
		checkMatch(path+".match", c.Match)
		if len(c.Conditions) == 0 {
			add(path+".conditions", "a group needs at least one condition")
		}
		if len(c.Conditions) > MaxConditions {
			add(path+".conditions", "at most %d conditions, got %d", MaxConditions, len(c.Conditions))
		}
		for j := range c.Conditions {
			inner := &c.Conditions[j]
			innerPath := fmt.Sprintf("%s.conditions[%d]", path, j)
			if inner.isGroup() {
				add(innerPath, "groups cannot be nested; use at most one level of groups")
				continue
			}
			validateLeaf(innerPath, inner, add)
		}
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

func validateLeaf(path string, c *Condition, add func(path, format string, args ...any)) {
	f, ok := fieldByName(c.Field)
	if !ok {
		add(path+".field", "unknown field %q; allowed: %s", c.Field, strings.Join(fieldNames(), ", "))
		return
	}
	if !slices.Contains(f.Ops, c.Op) {
		add(path+".op", "%q cannot be used with %s; allowed: %s", c.Op, f.Name, joinOps(f.Ops))
		return
	}
	vpath := path + ".value"

	switch c.Op {
	case OpEver, OpNever:
		if len(c.Value) > 0 && string(c.Value) != "null" {
			add(vpath, "%s takes no value; use null", c.Op)
		}
		c.Value = json.RawMessage("null")

	case OpWithinDays, OpNotWithinDays:
		n, ok := number(c.Value)
		if !ok || n != math.Trunc(n) || n < 1 || n > MaxDays {
			add(vpath, "%s needs a whole number of days between 1 and %d", c.Op, MaxDays)
			return
		}
		c.Value = mustJSON(int(n))

	case OpBetween:
		var pair []json.RawMessage
		if json.Unmarshal(c.Value, &pair) != nil || len(pair) != 2 {
			add(vpath, "between needs [low, high]")
			return
		}
		lo, ok1 := number(pair[0])
		hi, ok2 := number(pair[1])
		if !ok1 || !ok2 || lo < 0 || hi < lo {
			add(vpath, "between needs two non-negative numbers with low <= high")
			return
		}
		c.Value = mustJSON([]float64{lo, hi})

	case OpGt, OpGte, OpLt, OpLte:
		n, ok := number(c.Value)
		if !ok || n < 0 {
			add(vpath, "%s needs a non-negative number", f.Name)
			return
		}
		c.Value = mustJSON(n)

	case OpEq, OpNeq:
		if f.Kind == KindNumber {
			n, ok := number(c.Value)
			if !ok || n < 0 {
				add(vpath, "%s needs a non-negative number", f.Name)
				return
			}
			c.Value = mustJSON(n)
			return
		}
		var s string
		if json.Unmarshal(c.Value, &s) != nil {
			add(vpath, "%s needs one of: %s", f.Name, strings.Join(f.Values, ", "))
			return
		}
		canon, ok := canonical(f, s)
		if !ok {
			add(vpath, "unknown %s %q; allowed: %s", f.Name, s, strings.Join(f.Values, ", "))
			return
		}
		c.Value = mustJSON(canon)

	case OpIn, OpNotIn:
		var list []string
		if json.Unmarshal(c.Value, &list) != nil || len(list) == 0 {
			add(vpath, "%s needs a non-empty list of: %s", c.Op, strings.Join(f.Values, ", "))
			return
		}
		if len(list) > MaxInValues {
			add(vpath, "at most %d values", MaxInValues)
			return
		}
		out := make([]string, 0, len(list))
		for _, s := range list {
			canon, ok := canonical(f, s)
			if !ok {
				add(vpath, "unknown %s %q; allowed: %s", f.Name, s, strings.Join(f.Values, ", "))
				return
			}
			if !slices.Contains(out, canon) {
				out = append(out, canon)
			}
		}
		c.Value = mustJSON(out)
	}
}

// number reads a JSON number. Strings are not accepted: "30" and 30 should not
// both be valid, so mistakes surface instead of being guessed at.
func number(raw json.RawMessage) (float64, bool) {
	var n float64
	if json.Unmarshal(raw, &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	return n, true
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func joinOps(ops []Op) string {
	s := make([]string, len(ops))
	for i, op := range ops {
		s[i] = string(op)
	}
	return strings.Join(s, ", ")
}

// fold makes text comparable across case and Turkish letters, so "Istanbul",
// "istanbul" and "İSTANBUL" all match the stored "İstanbul".
var fold = strings.NewReplacer(
	"ç", "c", "Ç", "c", "ğ", "g", "Ğ", "g", "ı", "i", "I", "i", "İ", "i",
	"ö", "o", "Ö", "o", "ş", "s", "Ş", "s", "ü", "u", "Ü", "u",
)

func canonical(f Field, s string) (string, bool) {
	key := strings.ToLower(fold.Replace(strings.TrimSpace(s)))
	for _, v := range f.Values {
		if strings.ToLower(fold.Replace(v)) == key {
			return v, true
		}
	}
	return "", false
}
