package protocol

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Decode rejects unknown kinds/versions, duplicate fields at every depth,
// case-insensitive aliases, null fields and unknown fields (including nested).
func Decode(data []byte) (Record, error) {
	v, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	object, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("record must be an object")
	}
	if object["schema"] != Schema {
		return nil, fmt.Errorf("unsupported schema")
	}
	var record Record
	switch object["kind"] {
	case string(KindBaseline):
		record = &Baseline{}
	case string(KindPlan):
		record = &Plan{}
	case string(KindContract):
		record = &Contract{}
		if subject, ok := object["subject"].(map[string]any); ok {
			for key := range subject {
				if key != "issue" {
					return nil, fmt.Errorf("contract subject may only identify its issue")
				}
			}
		}
	case string(KindRun):
		record = &Run{}
	case string(KindEvidence):
		record = &Evidence{}
	case string(KindAcceptance):
		record = &Acceptance{}
	case string(KindPlanAcceptance):
		record = &PlanAcceptance{}
		if subject, ok := object["subject"].(map[string]any); ok {
			for key := range subject {
				if key != "milestone" {
					return nil, fmt.Errorf("plan acceptance subject may only identify its milestone")
				}
			}
		}
	case string(KindDelivery):
		record = &Delivery{}
	case string(KindApproval):
		record = &Approval{}
	default:
		return nil, fmt.Errorf("unsupported record kind %v", object["kind"])
	}
	if err := exactFields(v, reflect.TypeOf(record), "record"); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, record); err != nil {
		return nil, err
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	return record, nil
}

// Encode validates a typed record and emits canonical JSON, without a newline.
func Encode(record Record) ([]byte, error) {
	switch record.(type) {
	case *Baseline, *Plan, *Contract, *Run, *Evidence, *Acceptance, *PlanAcceptance, *Delivery, *Approval:
	default:
		return nil, fmt.Errorf("unsupported record type")
	}
	if reflect.ValueOf(record).IsNil() {
		return nil, fmt.Errorf("nil record")
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	if err := validStrings(reflect.ValueOf(record)); err != nil {
		return nil, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	// Use the same strict wire checks on outgoing and incoming records.
	if _, err := Decode(data); err != nil {
		return nil, err
	}
	return CanonicalJSON(data)
}

func DecodeComment(body string) (Record, error) {
	data, err := commentJSON(body)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

func EncodeComment(record Record) (string, error) {
	data, err := Encode(record)
	if err != nil {
		return "", err
	}
	body := Marker + " " + string(data)
	if len(body) > MaxRecordBytes {
		return "", fmt.Errorf("oversized record comment")
	}
	return body, nil
}

func commentJSON(body string) ([]byte, error) {
	if len(body) > MaxRecordBytes || !strings.HasPrefix(body, Marker) {
		return nil, fmt.Errorf("missing record marker or oversized comment")
	}
	return []byte(strings.TrimSpace(strings.TrimPrefix(body, Marker))), nil
}

func exactFields(value any, t reflect.Type, location string) error {
	if value == nil {
		return fmt.Errorf("%s: null is not a record field value", location)
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object", location)
		}
		fields := make(map[string]wireField)
		collectFields(t, fields)
		for key, child := range object {
			field, ok := fields[key]
			if !ok {
				return fmt.Errorf("%s: unknown field %q", location, key)
			}
			if err := exactFields(child, field.Type, location+"."+key); err != nil {
				return err
			}
		}
		for key, field := range fields {
			if _, exists := object[key]; !exists && !field.Optional {
				return fmt.Errorf("%s: missing field %q", location, key)
			}
		}
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array", location)
		}
		for i, child := range items {
			if err := exactFields(child, t.Elem(), fmt.Sprintf("%s[%d]", location, i)); err != nil {
				return err
			}
		}
	case reflect.Map, reflect.Interface:
		return fmt.Errorf("%s: open-ended fields are forbidden", location)
	}
	return nil
}

type wireField struct {
	Type     reflect.Type
	Optional bool
}

func collectFields(t reflect.Type, fields map[string]wireField) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			collectFields(f.Type, fields)
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")
		key := tag[0]
		if key != "" && key != "-" {
			optional := len(tag) > 1 && tag[1] == "omitempty"
			fields[key] = wireField{Type: f.Type, Optional: optional}
		}
	}
}

// json.Marshal silently replaces invalid UTF-8 in Go strings. Reject it before
// encoding to preserve the same lossless rules used for incoming wire bytes.
func validStrings(v reflect.Value) error {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return validStrings(v.Elem())
	}
	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return fmt.Errorf("invalid UTF-8 field")
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := validStrings(v.Field(i)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if err := validStrings(v.Index(i)); err != nil {
				return err
			}
		}
	}
	return nil
}
