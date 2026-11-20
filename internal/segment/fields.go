// Package segment turns audience rules into SQL safely.
//
// A rule is a small JSON document (from Claude or from the marketer's edits).
// It is checked against a JSON schema, then validated against the field catalog
// below, and only then compiled. The compiler takes column names and SQL
// fragments from this catalog, never from the rule, and passes every value as a
// query parameter. Text from a rule never becomes SQL.
//
// Consent is not a rule field: it is applied per channel when sending, so no
// rule can target users who did not consent.
package segment

import (
	"slices"

	"campaign-composer/internal/seed"
)

type Kind string

const (
	KindText   Kind = "text"   // one of a fixed list of values
	KindNumber Kind = "number" // non-negative number
	KindDate   Kind = "date"   // a timestamp, compared by "days ago"
)

type Op string

const (
	OpEq            Op = "eq"
	OpNeq           Op = "neq"
	OpIn            Op = "in"
	OpNotIn         Op = "not_in"
	OpGt            Op = "gt"
	OpGte           Op = "gte"
	OpLt            Op = "lt"
	OpLte           Op = "lte"
	OpBetween       Op = "between"
	OpWithinDays    Op = "within_days"     // happened in the last N days
	OpNotWithinDays Op = "not_within_days" // did not happen in the last N days (or never)
	OpEver          Op = "ever"
	OpNever         Op = "never"
)

var opsByKind = map[Kind][]Op{
	KindText:   {OpEq, OpNeq, OpIn, OpNotIn},
	KindNumber: {OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpBetween},
	KindDate:   {OpWithinDays, OpNotWithinDays, OpEver, OpNever},
}

// Field is one attribute a rule may filter on.
type Field struct {
	Name   string   `json:"name"`
	Label  string   `json:"label"`
	Kind   Kind     `json:"kind"`
	Ops    []Op     `json:"ops"`
	Values []string `json:"values,omitempty"` // allowed values for text fields
	column string   // the users column; never taken from input
}

// Limits keep rules small enough to read and cheap to run.
const (
	MaxConditions = 10   // per level
	MaxInValues   = 20   // values in an in / not_in list
	MaxDays       = 3650 // ten years
)

var catalog = []Field{
	{Name: "city", Label: "City", Kind: KindText, Values: seed.Cities(), column: "city"},
	{Name: "language", Label: "Language", Kind: KindText, Values: []string{"tr", "en"}, column: "language"},
	{Name: "order_count", Label: "Number of orders", Kind: KindNumber, column: "order_count"},
	{Name: "total_spent", Label: "Total spent (TRY)", Kind: KindNumber, column: "total_spent"},
	{Name: "last_purchase_at", Label: "Last purchase", Kind: KindDate, column: "last_purchase_at"},
	{Name: "last_cart_at", Label: "Last added to cart", Kind: KindDate, column: "last_cart_at"},
	{Name: "signed_up_at", Label: "Signed up", Kind: KindDate, column: "signed_up_at"},
}

func init() {
	for i := range catalog {
		catalog[i].Ops = opsByKind[catalog[i].Kind]
	}
}

// Fields returns the catalog, for the UI and the LLM prompt.
func Fields() []Field { return slices.Clone(catalog) }

func fieldByName(name string) (Field, bool) {
	for _, f := range catalog {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func fieldNames() []string {
	out := make([]string, len(catalog))
	for i, f := range catalog {
		out[i] = f.Name
	}
	return out
}

func allOps() []string {
	var out []string
	for _, k := range []Kind{KindText, KindNumber, KindDate} {
		for _, op := range opsByKind[k] {
			out = append(out, string(op))
		}
	}
	return out
}
