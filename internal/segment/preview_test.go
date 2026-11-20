package segment_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/seed"
	"campaign-composer/internal/segment"
	"campaign-composer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const users = 5000

func seeded(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testdb.New(t)
	if _, err := seed.Users(context.Background(), pool, seed.Options{N: users, Seed: 42, Now: now}); err != nil {
		t.Fatal(err)
	}
	return pool
}

func preview(t *testing.T, pool *pgxpool.Pool, raw string) segment.Preview {
	t.Helper()
	r, err := segment.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	q, err := segment.Compile(r, now)
	if err != nil {
		t.Fatal(err)
	}
	p, err := segment.PreviewQuery(context.Background(), pool, q)
	if err != nil {
		t.Fatalf("preview %s: %v", raw, err)
	}
	return p
}

func count(t *testing.T, pool *pgxpool.Pool, where string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM users WHERE "+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExampleBriefMatchesHandWrittenSQL(t *testing.T) {
	pool := seeded(t)
	p := preview(t, pool, `{"match":"all","conditions":[
		{"field":"city","op":"eq","value":"Istanbul"},
		{"field":"last_cart_at","op":"within_days","value":30},
		{"field":"last_purchase_at","op":"not_within_days","value":30}]}`)

	cut := now.AddDate(0, 0, -30)
	base := `city = 'İstanbul' AND last_cart_at >= $1 AND (last_purchase_at IS NULL OR last_purchase_at < $1)`
	if want := count(t, pool, base, cut); p.Total != want || want == 0 {
		t.Errorf("total = %d, hand-written SQL says %d", p.Total, want)
	}
	if want := count(t, pool, base+" AND sms_consent", cut); p.SMSReachable != want {
		t.Errorf("sms reachable = %d, want %d", p.SMSReachable, want)
	}
	if want := count(t, pool, base+" AND email_consent", cut); p.EmailReachable != want {
		t.Errorf("email reachable = %d, want %d", p.EmailReachable, want)
	}
	if len(p.Sample) != 5 {
		t.Fatalf("sample has %d users", len(p.Sample))
	}
	for _, s := range p.Sample {
		if s.City != "İstanbul" {
			t.Errorf("sample user %d is in %s", s.ID, s.City)
		}
	}
}

func TestOppositeConditionsPartitionUsers(t *testing.T) {
	pool := seeded(t)
	pairs := [][2]string{
		{`{"field":"city","op":"eq","value":"Ankara"}`, `{"field":"city","op":"neq","value":"Ankara"}`},
		{`{"field":"city","op":"in","value":["İzmir","Bursa"]}`, `{"field":"city","op":"not_in","value":["İzmir","Bursa"]}`},
		{`{"field":"last_purchase_at","op":"within_days","value":45}`, `{"field":"last_purchase_at","op":"not_within_days","value":45}`},
		{`{"field":"last_cart_at","op":"ever","value":null}`, `{"field":"last_cart_at","op":"never","value":null}`},
		{`{"field":"total_spent","op":"gte","value":1000}`, `{"field":"total_spent","op":"lt","value":1000}`},
	}
	for _, pair := range pairs {
		a := preview(t, pool, `{"match":"all","conditions":[`+pair[0]+`]}`).Total
		b := preview(t, pool, `{"match":"all","conditions":[`+pair[1]+`]}`).Total
		if a == 0 || b == 0 || a+b != users {
			t.Errorf("%s (%d) + its opposite (%d) = %d, want all %d users", pair[0], a, b, a+b, users)
		}
	}
}

func TestAnyGroupIsAUnion(t *testing.T) {
	pool := seeded(t)
	ankara := preview(t, pool, `{"match":"all","conditions":[{"field":"city","op":"eq","value":"Ankara"}]}`).Total
	izmir := preview(t, pool, `{"match":"all","conditions":[{"field":"city","op":"eq","value":"İzmir"}]}`).Total
	either := preview(t, pool, `{"match":"any","conditions":[
		{"field":"city","op":"eq","value":"Ankara"},{"field":"city","op":"eq","value":"İzmir"}]}`).Total
	nested := preview(t, pool, `{"match":"all","conditions":[{"match":"any","conditions":[
		{"field":"city","op":"eq","value":"Ankara"},{"field":"city","op":"eq","value":"İzmir"}]}]}`).Total
	if either != ankara+izmir || nested != either {
		t.Fatalf("Ankara %d + İzmir %d; any = %d, nested any = %d", ankara, izmir, either, nested)
	}
}

func TestEmptyRuleIsEveryone(t *testing.T) {
	pool := seeded(t)
	if p := preview(t, pool, `{"match":"all","conditions":[]}`); p.Total != users {
		t.Fatalf("total = %d", p.Total)
	}
}

// TestRandomRulesAlwaysRun builds many random valid rules from the catalog and
// runs them, so a SQL mistake in any field/operator combination shows up.
func TestRandomRulesAlwaysRun(t *testing.T) {
	pool := seeded(t)
	rng := rand.New(rand.NewPCG(1, 2))
	fields := segment.Fields()

	randomLeaf := func() map[string]any {
		f := fields[rng.IntN(len(fields))]
		op := f.Ops[rng.IntN(len(f.Ops))]
		var v any
		switch op {
		case segment.OpEq, segment.OpNeq:
			if f.Kind == segment.KindText {
				v = f.Values[rng.IntN(len(f.Values))]
			} else {
				v = rng.IntN(2000)
			}
		case segment.OpIn, segment.OpNotIn:
			v = []string{f.Values[rng.IntN(len(f.Values))], f.Values[rng.IntN(len(f.Values))]}
		case segment.OpGt, segment.OpGte, segment.OpLt, segment.OpLte:
			v = rng.Float64() * 3000
		case segment.OpBetween:
			lo := rng.IntN(1000)
			v = []int{lo, lo + rng.IntN(1000)}
		case segment.OpWithinDays, segment.OpNotWithinDays:
			v = 1 + rng.IntN(365)
		case segment.OpEver, segment.OpNever:
			v = nil
		}
		return map[string]any{"field": f.Name, "op": op, "value": v}
	}
	matches := []string{"all", "any"}

	for i := range 200 {
		var conds []any
		for range 1 + rng.IntN(4) {
			if rng.IntN(4) == 0 {
				var inner []any
				for range 1 + rng.IntN(3) {
					inner = append(inner, randomLeaf())
				}
				conds = append(conds, map[string]any{"match": matches[rng.IntN(2)], "conditions": inner})
			} else {
				conds = append(conds, randomLeaf())
			}
		}
		raw, _ := json.Marshal(map[string]any{"match": matches[rng.IntN(2)], "conditions": conds})
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			p := preview(t, pool, string(raw))
			if p.Total < 0 || p.Total > users || p.SMSReachable > p.Total || p.EmailReachable > p.Total {
				t.Fatalf("impossible counts %+v for %s", p, raw)
			}
		})
	}
}
