package schema

import (
	"strings"
	"testing"
)

const messageSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["sms", "channel"],
  "properties": {
    "sms": {"type": "string", "maxLength": 10},
    "channel": {"enum": ["sms", "email"]},
    "count": {"type": "integer", "minimum": 1}
  }
}`

func TestParseValid(t *testing.T) {
	s := MustCompile([]byte(messageSchema))
	obj, err := s.Parse(`{"sms": "hi there", "channel": "sms", "count": 3}`)
	if err != nil {
		t.Fatal(err)
	}
	if obj["sms"] != "hi there" {
		t.Fatalf("obj = %v", obj)
	}
}

func TestParseErrorsAreReadable(t *testing.T) {
	s := MustCompile([]byte(messageSchema))
	cases := map[string]struct{ in, want string }{
		"too long":      {`{"sms": "much too long", "channel": "sms"}`, "at /sms:"},
		"bad enum":      {`{"sms": "ok", "channel": "fax"}`, "at /channel:"},
		"below minimum": {`{"sms": "ok", "channel": "sms", "count": 0}`, "at /count:"},
		"extra field":   {`{"sms": "ok", "channel": "sms", "sql": "DROP TABLE"}`, "sql"},
		"missing field": {`{"sms": "ok"}`, "channel"},
		"not json":      {`{"sms": `, "not valid JSON"},
		"not an object": {`[1, 2]`, "JSON object"},
	}
	for name, c := range cases {
		_, err := s.Parse(c.in)
		if err == nil {
			t.Errorf("%s: want an error", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", name, err, c.want)
		}
		if strings.Contains(err.Error(), "schema.json") {
			t.Errorf("%s: error leaks the schema URL: %q", name, err)
		}
	}
}

func TestCompileRejectsBadSchemas(t *testing.T) {
	for name, raw := range map[string]string{
		"not object type": `{"type": "string"}`,
		"external ref":    `{"type": "object", "properties": {"a": {"$ref": "file:///etc/passwd"}}}`,
		"not json":        `{`,
	} {
		if _, err := Compile([]byte(raw)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
