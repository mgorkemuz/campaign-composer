package segment

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const exampleRule = `{
  "match": "all",
  "conditions": [
    {"field": "city", "op": "eq", "value": "istanbul"},
    {"field": "last_cart_at", "op": "within_days", "value": 30},
    {"field": "last_purchase_at", "op": "not_within_days", "value": 30}
  ]
}`

func mustParse(t *testing.T, raw string) Rule {
	t.Helper()
	r, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return r
}

func TestParseAndCompileExample(t *testing.T) {
	r := mustParse(t, exampleRule)
	if got := string(r.Conditions[0].Value); got != `"İstanbul"` {
		t.Errorf("city normalized to %s, want \"İstanbul\"", got)
	}
	q, err := Compile(r, now)
	if err != nil {
		t.Fatal(err)
	}
	want := "(u.city = $1 AND u.last_cart_at >= $2 AND (u.last_purchase_at IS NULL OR u.last_purchase_at < $3))"
	if q.Where != want {
		t.Errorf("where =\n  %s\nwant\n  %s", q.Where, want)
	}
	cutoff := now.Add(-30 * 24 * time.Hour)
	if len(q.Args) != 3 || q.Args[0] != "İstanbul" || q.Args[1] != cutoff || q.Args[2] != cutoff {
		t.Errorf("args = %v", q.Args)
	}
}

func TestCityNormalization(t *testing.T) {
	for _, in := range []string{"Istanbul", "istanbul", "ISTANBUL", "İSTANBUL", " İstanbul "} {
		r := mustParse(t, `{"match":"all","conditions":[{"field":"city","op":"eq","value":`+string(mustJSON(in))+`}]}`)
		if got := string(r.Conditions[0].Value); got != `"İstanbul"` {
			t.Errorf("%q normalized to %s", in, got)
		}
	}
	r := mustParse(t, `{"match":"all","conditions":[{"field":"city","op":"in","value":["izmir","İzmir","eskisehir"]}]}`)
	if got := string(r.Conditions[0].Value); got != `["İzmir","Eskişehir"]` {
		t.Errorf("in-list normalized to %s, want deduplicated canonical names", got)
	}
}

func TestGroupsCompile(t *testing.T) {
	r := mustParse(t, `{"match":"all","conditions":[
		{"field":"language","op":"eq","value":"tr"},
		{"match":"any","conditions":[
			{"field":"order_count","op":"between","value":[1,3]},
			{"field":"last_purchase_at","op":"never","value":null}]}]}`)
	q, err := Compile(r, now)
	if err != nil {
		t.Fatal(err)
	}
	want := "(u.language = $1 AND (u.order_count BETWEEN $2 AND $3 OR u.last_purchase_at IS NULL))"
	if q.Where != want {
		t.Fatalf("where = %s", q.Where)
	}
}

func TestEmptyRuleMatchesEveryone(t *testing.T) {
	q, err := Compile(mustParse(t, `{"match":"all","conditions":[]}`), now)
	if err != nil || q.Where != "TRUE" || len(q.Args) != 0 {
		t.Fatalf("q=%+v err=%v", q, err)
	}
}

func TestParseRejects(t *testing.T) {
	leaf := func(field, op, value string) string {
		return `{"match":"all","conditions":[{"field":"` + field + `","op":"` + op + `","value":` + value + `}]}`
	}
	cases := []struct {
		name, raw, mention string
	}{
		{"not json", `{"match":`, "not valid JSON"},
		{"bad match", `{"match":"some","conditions":[]}`, "match"},
		{"extra property", `{"match":"all","conditions":[],"sql":"DROP TABLE users"}`, "sql"},
		{"unknown field", leaf("password", "eq", `"x"`), "field"},
		{"injection as field", leaf("city = city OR 1=1 --", "eq", `"x"`), "field"},
		{"op for wrong kind", leaf("city", "gt", `5`), `"gt" cannot be used with city`},
		{"unknown city", leaf("city", "eq", `"Paris"`), `unknown city "Paris"`},
		{"injection as value", leaf("city", "eq", `"İstanbul' OR '1'='1"`), "unknown city"},
		{"days as string", leaf("last_cart_at", "within_days", `"30"`), "whole number of days"},
		{"zero days", leaf("last_cart_at", "within_days", `0`), "between 1 and"},
		{"fractional days", leaf("last_cart_at", "within_days", `1.5`), "whole number"},
		{"too many days", leaf("last_cart_at", "within_days", `3651`), "between 1 and"},
		{"negative number", leaf("total_spent", "gt", `-1`), "non-negative"},
		{"between reversed", leaf("total_spent", "between", `[500, 100]`), "low <= high"},
		{"between one value", leaf("total_spent", "between", `[100]`), "[low, high]"},
		{"empty in list", leaf("city", "in", `[]`), "non-empty list"},
		{"ever with value", leaf("last_purchase_at", "ever", `7`), "takes no value"},
		{"nested group", `{"match":"all","conditions":[{"match":"any","conditions":[
			{"match":"all","conditions":[{"field":"language","op":"eq","value":"tr"}]}]}]}`, "conditions"},
		{"empty group", `{"match":"all","conditions":[{"match":"any","conditions":[]}]}`, "at least one condition"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.raw))
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a ValidationError", c.name, err)
			continue
		}
		if !strings.Contains(err.Error(), c.mention) {
			t.Errorf("%s: %q does not mention %q", c.name, err, c.mention)
		}
	}
}

func TestTooManyConditions(t *testing.T) {
	conds := make([]string, MaxConditions+1)
	for i := range conds {
		conds[i] = `{"field":"language","op":"eq","value":"tr"}`
	}
	_, err := Parse([]byte(`{"match":"all","conditions":[` + strings.Join(conds, ",") + `]}`))
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	_, err := Parse([]byte(`{"match":"all","conditions":[
		{"field":"city","op":"eq","value":"Paris"},
		{"field":"total_spent","op":"gt","value":-5}]}`))
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 2 {
		t.Fatalf("err = %v; want both problems listed, so one round trip fixes them all", err)
	}
	if !strings.HasPrefix(ve.Problems[0], "conditions[0].value") || !strings.HasPrefix(ve.Problems[1], "conditions[1].value") {
		t.Fatalf("problems lack paths: %v", ve.Problems)
	}
}

func TestValuesNeverAppearInSQL(t *testing.T) {
	r := mustParse(t, `{"match":"any","conditions":[
		{"field":"city","op":"in","value":["Ankara","Trabzon"]},
		{"field":"language","op":"neq","value":"en"},
		{"field":"total_spent","op":"gte","value":1234.5}]}`)
	q, _ := Compile(r, now)
	for _, v := range []string{"Ankara", "Trabzon", "en'", "1234"} {
		if strings.Contains(q.Where, v) {
			t.Errorf("value %q was written into the SQL: %s", v, q.Where)
		}
	}
}

func TestWithOffset(t *testing.T) {
	args := make([]any, 11)
	parts := make([]string, 11)
	for i := range parts {
		parts[i] = "$" + string(mustJSON(i+1))
	}
	q := Query{Where: strings.Join(parts, ","), Args: args}.WithOffset(2)
	if q.Where != "$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13" {
		t.Fatalf("where = %s", q.Where)
	}
}

func TestSchemaListsCatalog(t *testing.T) {
	b, _ := json.Marshal(Schema())
	for _, f := range Fields() {
		if !strings.Contains(string(b), `"`+f.Name+`"`) {
			t.Errorf("schema is missing field %s", f.Name)
		}
	}
	for _, sensitive := range []string{"sms_consent", "email_consent", "email", "phone"} {
		if strings.Contains(string(b), `"`+sensitive+`"`) {
			t.Errorf("schema exposes %s as a rule field", sensitive)
		}
	}
}
