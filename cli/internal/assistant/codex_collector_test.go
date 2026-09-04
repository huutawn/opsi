package assistant

import (
	"errors"
	"strings"
	"testing"
)

func TestCodexEventCollector_AllEnvelopes(t *testing.T) {
	// 1. Error in result.content (JSON payload)
	t.Run("error_in_result_content", func(t *testing.T) {
		event := `{"type":"item.completed","item":{"id":"call-1","type":"mcp_tool_call","server":"opsi","tool":"deployments_list","status":"failed","result":{"isError":true,"content":[{"type":"text","text":"{\"code\":\"AUTH_REQUIRED\",\"message\":\"Opsi local session unauthenticated\"}"}]}}}` + "\n"
		parsed, err := parseCodexEventStreamWithTurnID("turn-101", []byte(event), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !parsed.ToolFailed {
			t.Fatal("expected ToolFailed to be true")
		}
		if !strings.Contains(parsed.ToolFailureMessage, "AUTH_REQUIRED") || !strings.Contains(parsed.ToolFailureMessage, "Opsi local session unauthenticated") {
			t.Fatalf("unexpected message: %s", parsed.ToolFailureMessage)
		}
		if parsed.EventInvalid {
			t.Fatal("expected EventInvalid to be false for valid payload")
		}
	})

	// 2. Error in output.structuredContent (camelCase)
	t.Run("error_in_output_structuredContent_camelCase", func(t *testing.T) {
		event := `{"type":"itemCompleted","item":{"callId":"call-2","type":"mcpToolCall","server":"opsi","toolName":"applications_list","state":"failed","output":{"isError":true,"structuredContent":{"code":"FORBIDDEN","message":"permission denied"}}}}` + "\n"
		parsed, err := parseCodexEventStreamWithTurnID("turn-102", []byte(event), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !parsed.ToolFailed {
			t.Fatalf("expected ToolFailed to be true, got parsed=%+v", parsed)
		}
		if !strings.Contains(parsed.ToolFailureMessage, "FORBIDDEN: permission denied") {
			t.Fatalf("unexpected message: %s", parsed.ToolFailureMessage)
		}
	})

	// 3. Error in top-level structured_content (snake_case)
	t.Run("error_in_toplevel_structured_content_snake_case", func(t *testing.T) {
		event := `{"type":"mcp_tool_call","id":"call-3","server":"opsi","name":"topology","status":"failed","is_error":true,"structured_content":{"code":"AUTHORITY_UNAVAILABLE","message":"Cloud authority is currently unavailable"}}` + "\n"
		parsed, err := parseCodexEventStreamWithTurnID("turn-103", []byte(event), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !parsed.ToolFailed {
			t.Fatal("expected ToolFailed to be true")
		}
		if !strings.Contains(parsed.ToolFailureMessage, "AUTHORITY_UNAVAILABLE: Cloud authority is currently unavailable") {
			t.Fatalf("unexpected message: %s", parsed.ToolFailureMessage)
		}
	})

	// 4. Sparse terminal event merging: rich event followed by sparse event
	t.Run("sparse_terminal_event_does_not_overwrite_diagnostic", func(t *testing.T) {
		events := strings.Join([]string{
			`{"type":"item.created","item":{"id":"call-4","type":"mcp_tool_call","server":"opsi","tool":"deployments_list","status":"in_progress"}}`,
			`{"type":"item.updated","item":{"id":"call-4","type":"mcp_tool_call","server":"opsi","tool":"deployments_list","result":{"isError":true,"structuredContent":{"code":"AUTH_REQUIRED","message":"Opsi local session unauthenticated"}}}}`,
			`{"type":"item.completed","item":{"id":"call-4","type":"mcp_tool_call","status":"failed"}}`, // sparse event without result or error
		}, "\n") + "\n"

		parsed, err := parseCodexEventStreamWithTurnID("turn-104", []byte(events), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !parsed.ToolFailed {
			t.Fatal("expected ToolFailed to be true")
		}
		if !strings.Contains(parsed.ToolFailureMessage, "AUTH_REQUIRED") || !strings.Contains(parsed.ToolFailureMessage, "Opsi local session unauthenticated") {
			t.Fatalf("diagnostic was lost by sparse terminal event: %s", parsed.ToolFailureMessage)
		}
		if parsed.EventInvalid {
			t.Fatal("event should not be marked invalid when diagnostic was captured earlier")
		}
	})

	// 5. Failed event with NO valid diagnostic payload -> ASSISTANT_MCP_EVENT_INVALID
	t.Run("failed_event_without_diagnostic_returns_invalid_event", func(t *testing.T) {
		sparseFailedEvent := `{"type":"item.completed","item":{"id":"call-5","type":"mcp_tool_call","server":"opsi","tool":"topology","status":"failed"}}` + "\n"
		parsed, err := parseCodexEventStreamWithTurnID("turn-debug-999", []byte(sparseFailedEvent), "")
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if !parsed.ToolFailed {
			t.Fatal("expected ToolFailed to be true")
		}
		if !parsed.EventInvalid {
			t.Fatal("expected EventInvalid to be true for failed call without diagnostic")
		}
		if !strings.Contains(parsed.ToolFailureMessage, "turn-debug-999") {
			t.Fatalf("expected turn ID in message for debugging, got: %s", parsed.ToolFailureMessage)
		}
		if !strings.Contains(parsed.ToolFailureMessage, "without valid diagnostic details") {
			t.Fatalf("expected diagnostic details note, got: %s", parsed.ToolFailureMessage)
		}
	})

	// 6. Duplicate events with same call_id: only counted once
	t.Run("duplicate_events_counted_once", func(t *testing.T) {
		events := strings.Join([]string{
			`{"type":"item.created","item":{"id":"call-6","type":"mcp_tool_call","server":"opsi","tool":"project_context","status":"in_progress"}}`,
			`{"type":"item.updated","item":{"id":"call-6","type":"mcp_tool_call","server":"opsi","tool":"project_context","status":"completed"}}`,
			`{"type":"item.completed","item":{"id":"call-6","type":"mcp_tool_call","server":"opsi","tool":"project_context","status":"completed"}}`,
		}, "\n") + "\n"

		parsed, err := parseCodexEventStreamWithTurnID("turn-106", []byte(events), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.SuccessfulOpsiToolCalls != 1 {
			t.Fatalf("expected exactly 1 successful tool call, got: %d", parsed.SuccessfulOpsiToolCalls)
		}
	})

	// 7. Null/nil values and malformed JSON lines
	t.Run("null_values_and_malformed_json_graceful", func(t *testing.T) {
		events := strings.Join([]string{
			`not a valid json line at all`,
			`{"type":null,"item":null}`,
			`{"type":"item.completed","item":{"id":"call-7","type":"mcp_tool_call","server":"opsi","tool":"topology","status":"completed","error":null,"result":null,"content":[]}}`,
			`{"broken json`,
		}, "\n") + "\n"

		parsed, err := parseCodexEventStreamWithTurnID("turn-107", []byte(events), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.SuccessfulOpsiToolCalls != 1 {
			t.Fatalf("expected 1 successful tool call, got: %d", parsed.SuccessfulOpsiToolCalls)
		}
	})

	// 8. Approval blocked event
	t.Run("approval_blocked_event", func(t *testing.T) {
		event := `{"type":"item.completed","item":{"type":"mcp_tool_call","server":"opsi","tool":"project_context","status":"approval_blocked"}}` + "\n"
		parsed, err := parseCodexEventStreamWithTurnID("turn-108", []byte(event), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !parsed.ApprovalBlocked {
			t.Fatal("expected ApprovalBlocked to be true")
		}
	})

	// 9. MCP start failure in stderr
	t.Run("mcp_start_failure_in_stderr", func(t *testing.T) {
		_, err := parseCodexEventStreamWithTurnID("turn-109", []byte{}, "error: failed to start mcp server 'opsi'")
		var aErr *AssistantError
		if !errors.As(err, &aErr) || aErr.Code != ErrAssistantMCPStartFailed {
			t.Fatalf("expected ASSISTANT_MCP_START_FAILED, got: %v", err)
		}
	})
}

func TestCodexEventCollector_StreamingProgressEvents(t *testing.T) {
	var progressEvents []ProgressEvent

	collector := newCodexEventCollector("turn-stream-1", func(pe ProgressEvent) {
		progressEvents = append(progressEvents, pe)
	})

	collector.emit(PhaseStartingProvider, "", "", "Đang khởi động tiến trình AI")
	collector.FeedStderrLine("Starting MCP server opsi...")

	collector.FeedStdoutLine([]byte(`{"type":"item.created","item":{"id":"call-1","type":"mcp_tool_call","server":"opsi","tool":"deployments_list","status":"in_progress"}}`))
	collector.FeedStdoutLine([]byte(`{"type":"item.completed","item":{"id":"call-1","type":"mcp_tool_call","server":"opsi","tool":"deployments_list","status":"completed"}}`))
	collector.FeedStdoutLine([]byte(`{"type":"item.created","item":{"type":"message"}}`))

	parsed, err := collector.Finish()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.SuccessfulOpsiToolCalls != 1 {
		t.Fatalf("expected 1 successful call, got %d", parsed.SuccessfulOpsiToolCalls)
	}

	if len(progressEvents) < 4 {
		t.Fatalf("expected at least 4 progress events, got %d: %+v", len(progressEvents), progressEvents)
	}

	phases := make([]string, len(progressEvents))
	for i, e := range progressEvents {
		phases[i] = e.Phase
		if e.Sequence != i+1 {
			t.Errorf("expected sequence %d, got %d", i+1, e.Sequence)
		}
	}

	expectedPhases := []string{PhaseStartingProvider, PhaseStartingMCP, PhaseToolRunning, PhaseToolSucceeded, PhaseGeneratingResponse}
	for _, expected := range expectedPhases {
		found := false
		for _, p := range phases {
			if p == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("phase %s was not emitted in progress events: %v", expected, phases)
		}
	}
}

func TestCodexEventCollector_IgnoresNonOpsiAndUnknownTools(t *testing.T) {
	events := []byte(
		`{"type":"item.completed","item":{"id":"call-shell","type":"tool_call","tool":"shell","status":"completed"}}` + "\n" +
			`{"type":"item.completed","item":{"id":"call-other","type":"mcp_tool_call","server":"other","tool":"deployments_list","status":"completed"}}` + "\n" +
			`{"type":"item.completed","item":{"id":"call-unknown","type":"mcp_tool_call","server":"opsi","tool":"unknown_tool","status":"completed"}}` + "\n",
	)
	parsed, err := parseCodexEventStream(events, "")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SuccessfulOpsiToolCalls != 0 || len(parsed.SuccessfulOpsiTools) != 0 {
		t.Fatalf("non-Opsi events satisfied grounding: %+v", parsed)
	}
}
