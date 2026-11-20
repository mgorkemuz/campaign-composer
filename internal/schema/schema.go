// Package schema validates JSON documents against a JSON Schema. Ported from the
// agents-platform tool-argument validator.
//
// Claude's structured outputs already guarantee the response's shape, but they
// cannot express constraints such as maxLength or minimum. This package is the
// backend's own check, and its error text is written so it can be sent back to
// the model for a corrected answer.
package schema

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Schema struct {
	compiled *jsonschema.Schema
}

// noLoader refuses every $ref to an external document; the default loader would
// read local files.
type noLoader struct{}

func (noLoader) Load(u string) (any, error) {
	return nil, fmt.Errorf("external schema references are not allowed: %s", u)
}

// Compile compiles an object schema.
func Compile(raw []byte) (*Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}
	if obj, ok := doc.(map[string]any); !ok || obj["type"] != "object" {
		return nil, errors.New(`schema must be a JSON object with "type": "object"`)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noLoader{})
	if err := c.AddResource("schema.json", doc); err != nil {
		return nil, err
	}
	compiled, err := c.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("invalid JSON schema: %w", err)
	}
	return &Schema{compiled}, nil
}

// MustCompile is Compile for schemas embedded in the program.
func MustCompile(raw []byte) *Schema {
	s, err := Compile(raw)
	if err != nil {
		panic(err)
	}
	return s
}

// Parse parses raw JSON and validates it. Numbers keep their exact text
// (json.Number), as the validator needs.
func (s *Schema) Parse(raw string) (map[string]any, error) {
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("not valid JSON: %v", err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("must be a JSON object")
	}
	if err := s.compiled.Validate(obj); err != nil {
		return nil, fmt.Errorf("does not match the schema: %s", describe(err))
	}
	return obj, nil
}

// describe flattens a validation error into "at /path: message" lines, dropping
// the schema URL the library prints first.
func describe(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	var lines []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			lines = append(lines, fmt.Sprintf("at %s: %s", loc, e.ErrorKind))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return strings.Join(lines, "; ")
}
