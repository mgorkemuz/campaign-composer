package segment

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Query is a compiled rule: a WHERE clause over the users table (aliased "u")
// whose values are all in Args, referenced as $1, $2, ...
type Query struct {
	Where string
	Args  []any
}

// Compile turns a rule from Parse into SQL. now anchors "within N days", so a
// preview and the send that follows it see the same cut-off if given the same
// time.
func Compile(r Rule, now time.Time) (Query, error) {
	c := compiler{now: now}
	where, err := c.group(r.Match, r.Conditions)
	if err != nil {
		return Query{}, err
	}
	return Query{Where: where, Args: c.args}, nil
}

// WithOffset renumbers placeholders so the query can follow n other parameters.
func (q Query) WithOffset(n int) Query {
	if n == 0 {
		return q
	}
	where := q.Where
	// Replace from the highest index down, so $1 never clobbers $10.
	for i := len(q.Args); i >= 1; i-- {
		where = strings.ReplaceAll(where, fmt.Sprintf("$%d", i), fmt.Sprintf("$#%d", i+n))
	}
	return Query{Where: strings.ReplaceAll(where, "$#", "$"), Args: q.Args}
}

type compiler struct {
	now  time.Time
	args []any
}

func (c *compiler) arg(v any) string {
	c.args = append(c.args, v)
	return fmt.Sprintf("$%d", len(c.args))
}

func (c *compiler) group(m Match, conds []Condition) (string, error) {
	if len(conds) == 0 {
		return "TRUE", nil // no conditions: everyone
	}
	parts := make([]string, 0, len(conds))
	for _, cond := range conds {
		var (
			sql string
			err error
		)
		if cond.isGroup() {
			sql, err = c.group(cond.Match, cond.Conditions)
		} else {
			sql, err = c.leaf(cond)
		}
		if err != nil {
			return "", err
		}
		parts = append(parts, sql)
	}
	join := " AND "
	if m == MatchAny {
		join = " OR "
	}
	return "(" + strings.Join(parts, join) + ")", nil
}

func (c *compiler) leaf(cond Condition) (string, error) {
	f, ok := fieldByName(cond.Field)
	if !ok {
		return "", fmt.Errorf("unknown field %q", cond.Field) // Parse rules this out
	}
	col := "u." + f.column

	switch cond.Op {
	case OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte:
		var v any
		if f.Kind == KindNumber {
			var n float64
			if err := json.Unmarshal(cond.Value, &n); err != nil {
				return "", err
			}
			v = n
		} else {
			var s string
			if err := json.Unmarshal(cond.Value, &s); err != nil {
				return "", err
			}
			v = s
		}
		sqlOp := map[Op]string{OpEq: "=", OpNeq: "<>", OpGt: ">", OpGte: ">=", OpLt: "<", OpLte: "<="}[cond.Op]
		return fmt.Sprintf("%s %s %s", col, sqlOp, c.arg(v)), nil

	case OpIn, OpNotIn:
		var list []string
		if err := json.Unmarshal(cond.Value, &list); err != nil {
			return "", err
		}
		if cond.Op == OpIn {
			return fmt.Sprintf("%s = ANY(%s)", col, c.arg(list)), nil
		}
		return fmt.Sprintf("NOT (%s = ANY(%s))", col, c.arg(list)), nil

	case OpBetween:
		var pair [2]float64
		if err := json.Unmarshal(cond.Value, &pair); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s BETWEEN %s AND %s", col, c.arg(pair[0]), c.arg(pair[1])), nil

	case OpWithinDays, OpNotWithinDays:
		var days int
		if err := json.Unmarshal(cond.Value, &days); err != nil {
			return "", err
		}
		cutoff := c.arg(c.now.Add(-time.Duration(days) * 24 * time.Hour))
		if cond.Op == OpWithinDays {
			return fmt.Sprintf("%s >= %s", col, cutoff), nil
		}
		return fmt.Sprintf("(%s IS NULL OR %s < %s)", col, col, cutoff), nil

	case OpEver:
		return col + " IS NOT NULL", nil
	case OpNever:
		return col + " IS NULL", nil
	}
	return "", fmt.Errorf("unknown op %q", cond.Op)
}
