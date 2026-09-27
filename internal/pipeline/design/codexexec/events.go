package codexexec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

var threadPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// EventError exposes only a fixed parser reason and line number. It deliberately
// contains no event text, provider message, prompt, raw field name or credential.
type EventError struct {
	Reason string
	Line   int
}

func (e *EventError) Error() string {
	return fmt.Sprintf("%s: %s at line %d", ErrEvent, e.Reason, e.Line)
}
func (e *EventError) Unwrap() error { return ErrEvent }

// parseEvents reads runtime envelopes, not fields inside model text. No
// model-supplied agent/run/thread identifier can become the runtime identity.
func parseEvents(data []byte) (Result, error) {
	var result Result
	started, completed, message := false, false, false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), protocol.MaxRecordBytes)
	count := 0
	failure := func(reason string) (Result, error) {
		return Result{}, &EventError{Reason: reason, Line: count}
	}
	for scanner.Scan() {
		count++
		if count > 4096 {
			return failure("event_count_limit")
		}
		if completed {
			return failure("event_after_completion")
		}
		canonical, err := protocol.CanonicalJSON(scanner.Bytes())
		if err != nil {
			return failure("invalid_event_json")
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(canonical, &event) != nil || event == nil {
			return failure("invalid_event_object")
		}
		var kind string
		if json.Unmarshal(event["type"], &kind) != nil {
			return failure("missing_event_type")
		}
		switch kind {
		case "thread.started":
			if result.ThreadID != "" || started || json.Unmarshal(event["thread_id"], &result.ThreadID) != nil || !threadPattern.MatchString(result.ThreadID) {
				return failure("invalid_thread_start")
			}
		case "turn.started":
			if result.ThreadID == "" || started {
				return failure("invalid_turn_start")
			}
			started = true
		case "item.started", "item.updated", "item.completed":
			if !started {
				return failure("item_before_turn")
			}
			var item map[string]json.RawMessage
			var itemType string
			if json.Unmarshal(event["item"], &item) != nil || json.Unmarshal(item["type"], &itemType) != nil {
				return failure("invalid_item_object")
			}
			// Tool, file-change, command, MCP, web and multi-agent events are
			// forbidden even if an unexpected host configuration exposes them.
			if itemType != "reasoning" && itemType != "agent_message" {
				return failure("forbidden_item_type")
			}
			if kind == "item.completed" && itemType == "agent_message" {
				var text string
				if message {
					return failure("multiple_final_messages")
				}
				if json.Unmarshal(item["text"], &text) != nil || len(text) == 0 {
					return failure("missing_final_text")
				}
				if len(text) > maxOutput {
					return failure("final_output_limit")
				}
				output, err := protocol.CanonicalJSON([]byte(text))
				if err != nil || len(output) == 0 || output[0] != '{' {
					return failure("invalid_final_json_object")
				}
				result.Output = []byte(text)
				message = true
			}
		case "turn.completed":
			if !started || !message {
				return failure("completion_without_final")
			}
			var usage map[string]json.RawMessage
			var input, output *int64
			if json.Unmarshal(event["usage"], &usage) != nil || json.Unmarshal(usage["input_tokens"], &input) != nil || json.Unmarshal(usage["output_tokens"], &output) != nil || input == nil || output == nil || *input < 0 || *output < 0 {
				return failure("invalid_runtime_usage")
			}
			result.InputTokens, result.OutputTokens = *input, *output
			completed = true
		case "turn.failed", "error":
			return failure("runtime_error_event")
		default:
			return failure("unknown_event_type")
		}
	}
	if scanner.Err() != nil {
		return failure("event_line_limit")
	}
	if !completed || result.ThreadID == "" {
		return failure("incomplete_stream")
	}
	return result, nil
}
