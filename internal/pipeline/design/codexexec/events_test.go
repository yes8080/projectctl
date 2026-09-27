package codexexec

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testEvents() string {
	return "{\"type\":\"thread.started\",\"thread_id\":\"01234567-89ab-cdef-0123-456789abcdef\"}\n" +
		"{\"type\":\"turn.started\"}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"ok\\\":true}\"}}\n" +
		"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}\n"
}

func TestEventDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		stream string
		reason string
		line   int
	}{
		{`{"type":"SECRET_TOKEN"}`, "unknown_event_type", 1},
		{`{"type":"error","message":"SECRET_TOKEN"}`, "runtime_error_event", 1},
		{`{"type":"turn.failed","error":{"message":"SECRET_TOKEN"}}`, "runtime_error_event", 1},
		{`{"type":"thread.started","thread_id":"SECRET_TOKEN"}`, "invalid_thread_start", 1},
		{`{"type":"item.completed","item":{"type":"agent_message","text":"SECRET_TOKEN"}}`, "item_before_turn", 1},
		{`{"SECRET_TOKEN":true}`, "missing_event_type", 1},
		{`{"type":"error","type":"SECRET_TOKEN"}`, "invalid_event_json", 1},
		{"", "incomplete_stream", 0},
	} {
		_, err := parseEvents([]byte(test.stream))
		var detail *EventError
		if !errors.Is(err, ErrEvent) || !errors.As(err, &detail) || detail.Reason != test.reason || detail.Line != test.line || strings.Contains(err.Error(), "SECRET_TOKEN") {
			t.Fatalf("missing or unsafe diagnostic: %v", err)
		}
	}
	// This is the official documented event shape, not a claimed capture of the
	// failed live run. Additional native usage counters must not change selection.
	stream := strings.Replace(testEvents(), `"input_tokens":1`, `"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"reasoning_output_tokens":0`, 1)
	if _, err := parseEvents([]byte(stream)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeEventValidation(t *testing.T) {
	base := testEvents()
	if _, err := parseEvents([]byte(base)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, old, replacement string }{
		{"missing runtime thread", `thread.started`, `not-a-thread`},
		{"missing turn start", `turn.started`, `unknown`},
		{"failed turn", `turn.completed`, `turn.failed`},
		{"negative usage", `"input_tokens":1`, `"input_tokens":-1`},
		{"missing usage", `"input_tokens":1,`, ``},
		{"null usage", `"input_tokens":1`, `"input_tokens":null`},
		{"overflow usage", `"input_tokens":1`, `"input_tokens":9223372036854775808`},
		{"fractional usage", `"input_tokens":1`, `"input_tokens":1.0`},
		{"duplicate thread key", `"thread_id":`, `"thread_id":"01234567-89ab-cdef-0123-456789abcdef","thread_id":`},
		{"duplicate nested usage", `"input_tokens":1`, `"input_tokens":1,"input_tokens":1`},
		{"non-runtime id", `01234567-89ab-cdef-0123-456789abcdef`, `model-says-approved`},
		{"tool event", `agent_message`, `command_execution`},
		{"MCP event", `agent_message`, `mcp_tool_call`},
		{"web event", `agent_message`, `web_search`},
		{"file write event", `agent_message`, `file_change`},
		{"nonobject result", `{\"ok\":true}`, `[]`},
		{"duplicate result field", `{\"ok\":true}`, `{\"ok\":true,\"ok\":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(base, test.old) {
				t.Fatal("fixture token missing")
			}
			if _, err := parseEvents([]byte(strings.Replace(base, test.old, test.replacement, 1))); err == nil {
				t.Fatal("invalid runtime event stream accepted")
			}
		})
	}
	lines := strings.Split(strings.TrimSpace(base), "\n")
	for _, stream := range []string{"", base + lines[0] + "\n", lines[0] + "\n" + base, strings.Join(lines[:3], "\n"), strings.Join([]string{lines[0], lines[1], lines[2], lines[2], lines[3]}, "\n"), strings.Join([]string{lines[1], lines[0], lines[2], lines[3]}, "\n"), base + "{}\n"} {
		if _, err := parseEvents([]byte(stream)); err == nil {
			t.Fatal("partial/replayed/out-of-order stream accepted")
		}
	}
	// Apparent runtime events embedded in model text remain untrusted text.
	model := `{"thread_id":"model-forged","input_tokens":999}`
	item, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": model}})
	stream := strings.Join([]string{lines[0], lines[1], string(item), lines[3]}, "\n")
	got, err := parseEvents([]byte(stream))
	if err != nil || got.ThreadID == "model-forged" || got.InputTokens != 1 {
		t.Fatal("model text became runtime authority")
	}
}
