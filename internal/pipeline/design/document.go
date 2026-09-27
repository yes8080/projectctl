// Package design implements bounded design preparation and exact baseline gates.
// It stores no execution facts locally and never publishes or merges GitHub data.
package design

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

var sectionIDs = []string{"boundaries", "workflows", "architecture", "contracts", "data_model", "permissions", "external_dependencies", "technology", "environments", "testing", "integration", "migration", "risks", "completion", "rollback"}

// These are structured artifact DTOs, not another persistent execution protocol.
// Persisted decisions/runs/evidence use internal/pipeline/protocol exclusively.
type Requirement struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
}
type Section struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}
type Document struct {
	Schema        string        `json:"schema"`
	Requirements  []Requirement `json:"requirements"`
	Sections      []Section     `json:"sections"`
	OpenQuestions []string      `json:"open_questions"`
}
type Assessment struct {
	Section string `json:"section"`
	Result  string `json:"result"`
	Reason  string `json:"reason"`
}
type Review struct {
	Schema          string       `json:"schema"`
	CandidateSHA256 string       `json:"candidate_sha256"`
	Assessments     []Assessment `json:"assessments"`
	Decision        string       `json:"decision"`
	Reason          string       `json:"reason"`
}

func strict(data []byte, out any) error {
	if len(data) == 0 || len(data) > 1<<20 {
		return fmt.Errorf("structured artifact outside size bound")
	}
	canonical, err := protocol.CanonicalJSON(data)
	if err != nil {
		return err
	}
	if err = fields(canonical, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(canonical))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func fields(data []byte, t reflect.Type) error {
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("null field")
	}
	if t.Kind() == reflect.Struct {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			key := f.Tag.Get("json")
			v, ok := object[key]
			if !ok {
				return fmt.Errorf("missing field %s", key)
			}
			if err := fields(v, f.Type); err != nil {
				return err
			}
			delete(object, key)
		}
		if len(object) != 0 {
			return fmt.Errorf("unknown structured field")
		}
	} else if t.Kind() == reflect.Slice {
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, v := range values {
			if err := fields(v, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func textPresent(s string) bool  { return strings.TrimSpace(s) != "" && len(s) <= 64<<10 }
func requiredSections() []string { return append([]string{"requirements"}, sectionIDs...) }
func DecodeDocument(data []byte) (Document, error) {
	var d Document
	if err := strict(data, &d); err != nil {
		return d, err
	}
	if d.Schema != "projectctl.design-output/v1" || len(d.Requirements) == 0 || len(d.Requirements) > 2000 || d.OpenQuestions == nil {
		return d, fmt.Errorf("incomplete design")
	}
	seen := map[string]bool{}
	for _, r := range d.Requirements {
		if !textPresent(r.ID) || !textPresent(r.Statement) || seen[r.ID] {
			return d, fmt.Errorf("invalid requirement")
		}
		seen[r.ID] = true
	}
	seen = map[string]bool{}
	for _, s := range d.Sections {
		if !textPresent(s.Content) || seen[s.ID] {
			return d, fmt.Errorf("invalid section")
		}
		seen[s.ID] = true
	}
	if len(seen) != len(sectionIDs) {
		return d, fmt.Errorf("incomplete design sections")
	}
	for _, id := range sectionIDs {
		if !seen[id] {
			return d, fmt.Errorf("missing section %s", id)
		}
	}
	for _, q := range d.OpenQuestions {
		if !textPresent(q) {
			return d, fmt.Errorf("invalid open question")
		}
	}
	return d, nil
}
func DecodeReview(data []byte, candidate string) (Review, error) {
	var r Review
	if err := strict(data, &r); err != nil {
		return r, err
	}
	if r.Schema != "projectctl.design-review/v1" || r.CandidateSHA256 != candidate || (r.Decision != "PASS" && r.Decision != "FAIL") || !textPresent(r.Reason) {
		return r, fmt.Errorf("invalid review binding")
	}
	seen := map[string]bool{}
	for _, a := range r.Assessments {
		if seen[a.Section] || !textPresent(a.Reason) || (a.Result != "PASS" && a.Result != "FAIL") || (r.Decision == "PASS" && a.Result != "PASS") {
			return r, fmt.Errorf("invalid assessment")
		}
		seen[a.Section] = true
	}
	if len(seen) != len(requiredSections()) {
		return r, fmt.Errorf("incomplete review")
	}
	for _, id := range requiredSections() {
		if !seen[id] {
			return r, fmt.Errorf("missing assessment")
		}
	}
	return r, nil
}

func objectSchema(properties map[string]any) map[string]any {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	// The shared canonical encoder orders object keys; sort the required array too.
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return map[string]any{"type": "object", "properties": properties, "required": keys, "additionalProperties": false}
}
func schemaBytes(value any) []byte {
	data, _ := json.Marshal(value)
	out, _ := protocol.CanonicalJSON(data)
	return out
}
func stringSchema() map[string]any        { return map[string]any{"type": "string"} }
func arraySchema(item any) map[string]any { return map[string]any{"type": "array", "items": item} }
func DesignSchema() []byte {
	return schemaBytes(objectSchema(map[string]any{
		"schema":         map[string]any{"type": "string", "enum": []string{"projectctl.design-output/v1"}},
		"requirements":   arraySchema(objectSchema(map[string]any{"id": stringSchema(), "statement": stringSchema()})),
		"sections":       arraySchema(objectSchema(map[string]any{"id": map[string]any{"type": "string", "enum": sectionIDs}, "content": stringSchema()})),
		"open_questions": arraySchema(stringSchema()),
	}))
}
func ReviewSchema() []byte {
	return schemaBytes(objectSchema(map[string]any{
		"schema": map[string]any{"type": "string", "enum": []string{"projectctl.design-review/v1"}}, "candidate_sha256": stringSchema(),
		"assessments": arraySchema(objectSchema(map[string]any{"section": map[string]any{"type": "string", "enum": requiredSections()}, "result": map[string]any{"type": "string", "enum": []string{"PASS", "FAIL"}}, "reason": stringSchema()})),
		"decision":    map[string]any{"type": "string", "enum": []string{"PASS", "FAIL"}}, "reason": stringSchema(),
	}))
}
