package assistant

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/mcp"
)

// Typed Assistant error codes added for event validation
const (
	ErrAssistantMCPEventInvalid = "ASSISTANT_MCP_EVENT_INVALID"
)

// Progress phases
const (
	PhaseQueued             = "queued"
	PhaseStartingProvider   = "starting_provider"
	PhaseStartingMCP        = "starting_mcp"
	PhaseToolRunning        = "tool_running"
	PhaseToolSucceeded      = "tool_succeeded"
	PhaseToolFailed         = "tool_failed"
	PhaseGeneratingResponse = "generating_response"
	PhaseSucceeded          = "succeeded"
	PhaseFailed             = "failed"
)

// ProgressEvent represents a safe progress step emitted to manager/UI.
// Must contain only sequence, timestamp, phase, allowlisted tool name, safe error code, and filtered summary.
// Never contains raw JSONL, raw stderr, tool input, or tool result.
type ProgressEvent struct {
	Sequence  int       `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Phase     string    `json:"phase"`
	Tool      string    `json:"tool,omitempty"`
	Code      string    `json:"code,omitempty"`
	Summary   string    `json:"summary"`
}

var allowedOpsiToolsMap map[string]bool
var allowedOpsiToolsOnce sync.Once

func isAllowedOpsiTool(name string) bool {
	allowedOpsiToolsOnce.Do(func() {
		allowedOpsiToolsMap = make(map[string]bool)
		for _, tool := range mcp.AllTools() {
			allowedOpsiToolsMap[tool.Name] = true
		}
	})
	return allowedOpsiToolsMap[name]
}

func sanitizeToolName(name string) string {
	toolName := strings.TrimSpace(name)
	if strings.HasPrefix(toolName, "opsi__") {
		toolName = strings.TrimPrefix(toolName, "opsi__")
	} else if strings.HasPrefix(toolName, "opsi.") {
		toolName = strings.TrimPrefix(toolName, "opsi.")
	}
	return toolName
}

func toolFriendlySummary(toolName, status string) string {
	switch toolName {
	case "deployments_list":
		if status == "running" {
			return "Reading deployment history"
		}
		return "Deployment history read"
	case "deployment_get":
		if status == "running" {
			return "Reading deployment details"
		}
		return "Deployment details read"
	case "topology":
		if status == "running" {
			return "Reading topology"
		}
		return "Topology read"
	case "project_context", "project_review_context":
		if status == "running" {
			return "Reading project context"
		}
		return "Project context read"
	case "validate_service_configuration_proposal":
		if status == "running" {
			return "Validating service configuration"
		}
		return "Service configuration validated"
	case "validate_source_patch_proposal":
		if status == "running" {
			return "Validating source patch"
		}
		return "Source patch validated"
	case "services_list", "applications_list":
		if status == "running" {
			return "Reading applications"
		}
		return "Applications read"
	case "build_records_list", "build_record_get":
		if status == "running" {
			return "Reading build history"
		}
		return "Build history read"
	case "source_risk_report":
		if status == "running" {
			return "Analyzing source risks"
		}
		return "Source risk report read"
	case "dependency_analysis_context":
		if status == "running" {
			return "Analyzing dependencies"
		}
		return "Dependency analysis read"
	default:
		if !isAllowedOpsiTool(toolName) {
			if status == "running" {
				return "Using Opsi MCP"
			}
			return "Opsi MCP step completed"
		}
		if status == "running" {
			return fmt.Sprintf("Running %s", toolName)
		}
		return fmt.Sprintf("Completed %s", toolName)
	}
}

type mcpCallRecord struct {
	callID            string
	toolName          string
	server            string
	status            string // "in_progress", "completed", "failed", "approval_blocked"
	isTerminal        bool
	hasError          bool
	hasDiagnostic     bool
	diagnosticCode    string
	diagnosticMessage string
	retryable         bool
	nextAction        string
	arguments         map[string]any
	result            map[string]any
	content           any
}

type codexEventCollector struct {
	turnID     string
	onProgress func(ProgressEvent)
	sequence   int
	mu         sync.Mutex

	threadID        string
	approvalBlocked bool
	mcpStartFailed  bool

	callStates map[string]*mcpCallRecord
	callOrder  []string

	seenRunning   map[string]bool
	seenCompleted map[string]bool

	validatedProposals     []validatedProposalRecord
	validatedSourcePatches []validatedSourcePatchRecord

	hasEmittedGen bool
}

func newCodexEventCollector(turnID string, onProgress func(ProgressEvent)) *codexEventCollector {
	return &codexEventCollector{
		turnID:        strings.TrimSpace(turnID),
		onProgress:    onProgress,
		callStates:    make(map[string]*mcpCallRecord),
		seenRunning:   make(map[string]bool),
		seenCompleted: make(map[string]bool),
	}
}

func (c *codexEventCollector) emit(phase, tool, code, summary string) {
	if c.onProgress == nil {
		return
	}
	c.sequence++
	allowlistedTool := ""
	if isAllowedOpsiTool(tool) {
		allowlistedTool = tool
	}
	c.onProgress(ProgressEvent{
		Sequence:  c.sequence,
		Timestamp: time.Now().UTC(),
		Phase:     phase,
		Tool:      allowlistedTool,
		Code:      code,
		Summary:   summary,
	})
}

func (c *codexEventCollector) FeedStderrLine(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	trimmed := strings.TrimSpace(line)
	if isMCPStartFailure(trimmed) {
		c.mcpStartFailed = true
		c.emit(PhaseToolFailed, "", ErrAssistantMCPStartFailed, "Opsi MCP server failed to start")
	} else if strings.Contains(strings.ToLower(trimmed), "starting mcp") {
		c.emit(PhaseStartingMCP, "", "", "Starting Opsi MCP server")
	}
}

func (c *codexEventCollector) FeedStderr(output string) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		c.FeedStderrLine(scanner.Text())
	}
}

func (c *codexEventCollector) FeedStdoutLine(lineBytes []byte) {
	line := bytes.TrimSpace(lineBytes)
	if len(line) == 0 {
		return
	}

	var generic map[string]any
	if err := json.Unmarshal(line, &generic); err != nil {
		return // malformed JSON lines are ignored
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	eventType, _ := generic["type"].(string)
	if eventType == "thread.started" || eventType == "threadStarted" {
		if tid, ok := generic["thread_id"].(string); ok && strings.TrimSpace(tid) != "" {
			c.threadID = strings.TrimSpace(tid)
		} else if tid, ok := generic["threadId"].(string); ok && strings.TrimSpace(tid) != "" {
			c.threadID = strings.TrimSpace(tid)
		}
	}

	if eventType == "error" {
		if errMsg, ok := generic["message"].(string); ok && isApprovalBlocked(errMsg) {
			c.approvalBlocked = true
		}
	}

	// Determine item container: nested "item" or top-level generic object
	item, _ := generic["item"].(map[string]any)
	if item == nil {
		item = generic
	}

	itemType, _ := item["type"].(string)
	if itemType == "" {
		itemType, _ = generic["type"].(string)
	}

	// Check if this event indicates message generation started
	if (eventType == "item.created" || eventType == "itemCreated") && (itemType == "message" || itemType == "agent_message") {
		if !c.hasEmittedGen {
			c.hasEmittedGen = true
			c.emit(PhaseGeneratingResponse, "", "", "Generating response")
		}
	}

	// Detect tool calls across type variants
	if isToolCallType(itemType) || isToolCallType(eventType) {
		c.processToolCallEvent(item, generic)
	}
}

func (c *codexEventCollector) FeedStdout(events []byte) {
	scanner := bufio.NewScanner(bytes.NewReader(events))
	for scanner.Scan() {
		c.FeedStdoutLine(scanner.Bytes())
	}
}

func isToolCallType(t string) bool {
	switch t {
	case "mcp_tool_call", "tool_call", "mcp_call", "mcp_tool_result",
		"mcpToolCall", "toolCall", "mcpCall", "mcpToolResult":
		return true
	default:
		return false
	}
}

func (c *codexEventCollector) processToolCallEvent(item, generic map[string]any) {
	status := extractStatus(item, generic)
	_, hasDiag, _, msg, _, _ := extractDiagnostic(item, generic)
	errVal := extractErrorField(item, generic)
	if status == "approval_blocked" || (!hasDiag && (isApprovalBlocked(msg) || isApprovalBlocked(fmt.Sprint(errVal)))) {
		c.approvalBlocked = true
	}

	callID := extractCallID(item, generic)
	if callID == "" {
		return // ungrounded event without identity
	}

	toolName := extractToolName(item, generic)
	toolName = sanitizeToolName(toolName)
	server := extractServer(item, generic)
	rawToolName := extractToolName(item, generic)
	if strings.HasPrefix(rawToolName, "opsi__") || strings.HasPrefix(rawToolName, "opsi.") {
		server = "opsi"
	}

	// Grounding accepts only the canonical Opsi tool allowlist. A missing server
	// field is tolerated because Codex versions differ, but arbitrary provider or
	// native tool events must never satisfy the grounding invariant.
	if (server != "" && server != "opsi") || !isAllowedOpsiTool(toolName) {
		return
	}

	rec, exists := c.callStates[callID]
	if !exists {
		rec = &mcpCallRecord{
			callID:   callID,
			toolName: toolName,
			server:   server,
		}
		c.callStates[callID] = rec
		c.callOrder = append(c.callOrder, callID)
	} else {
		if rec.toolName == "" && toolName != "" {
			rec.toolName = toolName
		}
		if rec.server == "" && server != "" {
			rec.server = server
		}
	}

	// Extract and merge arguments
	args := extractArguments(item, generic)
	if args != nil {
		if rec.arguments == nil {
			rec.arguments = args
		} else {
			for k, v := range args {
				rec.arguments[k] = v
			}
		}
	}

	// Extract and merge result/output
	res := extractResult(item, generic)
	if res != nil {
		if rec.result == nil {
			rec.result = res
		} else {
			for k, v := range res {
				rec.result[k] = v
			}
		}
	}

	// Extract content
	if content := extractContent(item, generic); content != nil {
		rec.content = content
	}

	// Extract error / diagnostic info
	hasErr, hasDiag, code, msg, retryable, nextAction := extractDiagnostic(item, generic)
	if hasDiag {
		rec.hasDiagnostic = true
		rec.diagnosticCode = redactDiagnostic(code)
		rec.diagnosticMessage = redactDiagnostic(msg)
		rec.retryable = retryable
		rec.nextAction = redactDiagnostic(nextAction)
	}
	if hasErr {
		rec.hasError = true
	}

	// Determine status
	status = extractStatus(item, generic)
	if status == "approval_blocked" || isApprovalBlocked(msg) {
		c.approvalBlocked = true
		rec.status = "approval_blocked"
		rec.isTerminal = true
		return
	}

	if status == "failed" || status == "error" || rec.hasError {
		rec.status = "failed"
		rec.hasError = true
		rec.isTerminal = true
		if !c.seenCompleted[callID] {
			c.seenCompleted[callID] = true
			toolLabel := "Opsi MCP"
			if isAllowedOpsiTool(rec.toolName) {
				toolLabel = rec.toolName
			}
			c.emit(PhaseToolFailed, rec.toolName, rec.diagnosticCode, fmt.Sprintf("%s failed: %s", toolLabel, codeOrMessage(rec.diagnosticCode, rec.diagnosticMessage)))
		}
		return
	}

	if status == "completed" || status == "success" || status == "succeeded" {
		rec.status = "completed"
		rec.isTerminal = true
		if !c.seenCompleted[callID] {
			c.seenCompleted[callID] = true
			c.emit(PhaseToolSucceeded, rec.toolName, "", toolFriendlySummary(rec.toolName, "succeeded"))
		}
		return
	}

	if status == "in_progress" || status == "running" || status == "" {
		if rec.status == "" {
			rec.status = "in_progress"
		}
		if !c.seenRunning[callID] {
			c.seenRunning[callID] = true
			c.emit(PhaseToolRunning, rec.toolName, "", toolFriendlySummary(rec.toolName, "running"))
		}
	}
}

func extractCallID(item, generic map[string]any) string {
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, key := range []string{"call_id", "callId", "id", "item_id", "itemId"} {
			if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

func extractToolName(item, generic map[string]any) string {
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, key := range []string{"tool", "name", "tool_name", "toolName"} {
			if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

func extractServer(item, generic map[string]any) string {
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, key := range []string{"server", "server_name", "serverName"} {
			if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

func extractStatus(item, generic map[string]any) string {
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, key := range []string{"status", "state"} {
			if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.ToLower(strings.TrimSpace(v))
			}
		}
	}
	return ""
}

func extractArguments(item, generic map[string]any) map[string]any {
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, key := range []string{"arguments", "input", "params"} {
			if v, ok := m[key].(map[string]any); ok {
				return v
			}
		}
	}
	return nil
}

func extractResult(item, generic map[string]any) map[string]any {
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, key := range []string{"result", "output"} {
			if v, ok := m[key].(map[string]any); ok {
				return v
			}
		}
	}
	return nil
}

func extractContent(item, generic map[string]any) any {
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, key := range []string{"content", "contents"} {
			if v, ok := m[key]; ok && v != nil {
				return v
			}
		}
		// Check inside result/output
		for _, resKey := range []string{"result", "output"} {
			if res, ok := m[resKey].(map[string]any); ok {
				for _, key := range []string{"content", "contents"} {
					if v, ok := res[key]; ok && v != nil {
						return v
					}
				}
			}
		}
	}
	return nil
}

func extractDiagnostic(item, generic map[string]any) (hasError bool, hasDiagnostic bool, code string, message string, retryable bool, nextAction string) {
	// Check is_error / isError
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		if isErr, ok := m["is_error"].(bool); ok && isErr {
			hasError = true
		}
		if isErr, ok := m["isError"].(bool); ok && isErr {
			hasError = true
		}
		if res, ok := m["result"].(map[string]any); ok {
			if isErr, ok := res["is_error"].(bool); ok && isErr {
				hasError = true
			}
			if isErr, ok := res["isError"].(bool); ok && isErr {
				hasError = true
			}
		}
		if out, ok := m["output"].(map[string]any); ok {
			if isErr, ok := out["is_error"].(bool); ok && isErr {
				hasError = true
			}
			if isErr, ok := out["isError"].(bool); ok && isErr {
				hasError = true
			}
		}
	}

	// Check structuredContent / structured_content across all levels
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, scKey := range []string{"structuredContent", "structured_content"} {
			if sc, ok := m[scKey].(map[string]any); ok && len(sc) > 0 {
				c, _ := sc["code"].(string)
				msg, _ := sc["message"].(string)
				if strings.TrimSpace(c) != "" || strings.TrimSpace(msg) != "" {
					retryable, _ = sc["retryable"].(bool)
					nextAction, _ = sc["next_action"].(string)
					return true, true, strings.TrimSpace(c), strings.TrimSpace(msg), retryable, strings.TrimSpace(nextAction)
				}
			}
		}
		for _, resKey := range []string{"result", "output"} {
			if res, ok := m[resKey].(map[string]any); ok {
				for _, scKey := range []string{"structuredContent", "structured_content"} {
					if sc, ok := res[scKey].(map[string]any); ok && len(sc) > 0 {
						c, _ := sc["code"].(string)
						msg, _ := sc["message"].(string)
						if strings.TrimSpace(c) != "" || strings.TrimSpace(msg) != "" {
							retryable, _ = sc["retryable"].(bool)
							nextAction, _ = sc["next_action"].(string)
							return true, true, strings.TrimSpace(c), strings.TrimSpace(msg), retryable, strings.TrimSpace(nextAction)
						}
					}
				}
			}
		}
	}

	// Check content JSON text
	content := extractContent(item, generic)
	if text := extractTextFromContent(content); text != "" {
		var payload struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			Retryable  bool   `json:"retryable"`
			NextAction string `json:"next_action"`
		}
		if json.Unmarshal([]byte(text), &payload) == nil && (strings.TrimSpace(payload.Code) != "" || strings.TrimSpace(payload.Message) != "") {
			return true, true, strings.TrimSpace(payload.Code), strings.TrimSpace(payload.Message), payload.Retryable, strings.TrimSpace(payload.NextAction)
		}
		// If text is not JSON but isError was true, the text itself might be diagnostic message
		if hasError && strings.TrimSpace(text) != "" {
			return true, true, "", strings.TrimSpace(text), false, ""
		}
	}

	// Check error / err / errorMessage fields
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, errKey := range []string{"error", "err", "error_message", "errorMessage"} {
			if val, ok := m[errKey]; ok && val != nil {
				hasError = true
				if str, ok := val.(string); ok && strings.TrimSpace(str) != "" && strings.TrimSpace(str) != "<nil>" {
					var payload struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					}
					if json.Unmarshal([]byte(str), &payload) == nil && (payload.Code != "" || payload.Message != "") {
						return true, true, strings.TrimSpace(payload.Code), strings.TrimSpace(payload.Message), false, ""
					}
					return true, true, "", strings.TrimSpace(str), false, ""
				} else if obj, ok := val.(map[string]any); ok {
					c, _ := obj["code"].(string)
					msg, _ := obj["message"].(string)
					if c != "" || msg != "" {
						return true, true, strings.TrimSpace(c), strings.TrimSpace(msg), false, ""
					}
				}
			}
		}
	}

	return hasError, false, "", "", false, ""
}

func extractErrorField(item, generic map[string]any) any {
	for _, m := range []map[string]any{item, generic} {
		if m == nil {
			continue
		}
		for _, errKey := range []string{"error", "err", "error_message", "errorMessage"} {
			if val, ok := m[errKey]; ok && val != nil {
				return val
			}
		}
	}
	return nil
}

func extractTextFromContent(value any) string {
	if value == nil {
		return ""
	}
	if str, ok := value.(string); ok {
		return strings.TrimSpace(str)
	}
	if list, ok := value.([]any); ok {
		for _, entry := range list {
			if item, ok := entry.(map[string]any); ok {
				if t, ok := item["text"].(string); ok && strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			}
		}
	}
	return ""
}

func codeOrMessage(code, message string) string {
	code, message = strings.TrimSpace(code), strings.TrimSpace(message)
	if code != "" && message != "" {
		return code + ": " + message
	}
	if code != "" {
		return code
	}
	if message != "" {
		return message
	}
	return "unknown error"
}

func (c *codexEventCollector) Finish() (codexParsedEvents, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result codexParsedEvents
	result.ThreadID = c.threadID

	if c.mcpStartFailed {
		return result, &AssistantError{
			Code:    ErrAssistantMCPStartFailed,
			Message: "failed to start required Opsi MCP server",
		}
	}

	if c.approvalBlocked {
		result.ApprovalBlocked = true
		return result, nil
	}

	toolSet := make(map[string]bool)

	// Iterate through calls in recorded order
	for _, callID := range c.callOrder {
		rec := c.callStates[callID]
		if rec == nil {
			continue
		}

		if rec.status == "approval_blocked" {
			result.ApprovalBlocked = true
			return result, nil
		}

		if rec.hasError || rec.status == "failed" || rec.status == "error" {
			result.ToolFailed = true
			if rec.hasDiagnostic && (rec.diagnosticCode != "" || rec.diagnosticMessage != "") {
				diag := codeOrMessage(rec.diagnosticCode, rec.diagnosticMessage)
				result.ToolFailureMessage = fmt.Sprintf("Opsi MCP tool %q failed: %s", rec.toolName, diag)
				result.ToolFailureCode = rec.diagnosticCode
				result.ToolFailureRetryable = rec.retryable
				result.ToolFailureNextAction = rec.nextAction
				return result, nil
			}
			// Event failed without valid diagnostic payload
			result.EventInvalid = true
			turnMsg := c.turnID
			if turnMsg == "" {
				turnMsg = "unknown"
			}
			result.ToolFailureMessage = fmt.Sprintf("turn %s: Opsi MCP tool %q reported failure without valid diagnostic details", turnMsg, rec.toolName)
			return result, nil
		}

		if rec.status == "completed" || rec.status == "success" {
			result.SuccessfulOpsiToolCalls++
			if rec.toolName != "" && !toolSet[rec.toolName] {
				toolSet[rec.toolName] = true
				result.SuccessfulOpsiTools = append(result.SuccessfulOpsiTools, rec.toolName)
			}
			if rec.toolName == "validate_service_configuration_proposal" {
				if vRec := extractValidationRecordFromCall(rec); vRec != nil {
					result.ValidatedProposals = append(result.ValidatedProposals, *vRec)
				}
			}
			if rec.toolName == "validate_source_patch_proposal" {
				if spRec := extractSourcePatchValidationRecordFromCall(rec); spRec != nil {
					result.ValidatedSourcePatches = append(result.ValidatedSourcePatches, *spRec)
				}
			}
		}
	}

	sort.Strings(result.SuccessfulOpsiTools)
	return result, nil
}

func extractValidationRecordFromCall(rec *mcpCallRecord) *validatedProposalRecord {
	args := rec.arguments
	if args == nil {
		return nil
	}

	var proposalObj map[string]any
	if p, ok := args["proposal"].(map[string]any); ok {
		proposalObj = p
	} else {
		proposalObj = args
	}

	if proposalObj == nil {
		return nil
	}

	appID, _ := proposalObj["application_id"].(string)
	envID, _ := proposalObj["environment_id"].(string)
	expRevNum, _ := proposalObj["expected_revision"].(float64)
	expStateHash, _ := proposalObj["expected_state_hash"].(string)
	analysisHash, _ := proposalObj["analysis_inputs_hash"].(string)
	draftRaw := proposalObj["draft"]
	draftHash := canonicalDraftHash(draftRaw)

	status := ""
	if rec.result != nil {
		if st, ok := rec.result["status"].(string); ok && st != "" {
			status = st
		}
	}
	if status == "" && rec.content != nil {
		if text := extractTextFromContent(rec.content); text != "" {
			var parsedRes struct {
				Status string `json:"status"`
			}
			if json.Unmarshal([]byte(text), &parsedRes) == nil && parsedRes.Status != "" {
				status = parsedRes.Status
			}
		}
	}

	if strings.TrimSpace(appID) == "" || strings.TrimSpace(envID) == "" || strings.TrimSpace(expStateHash) == "" || strings.TrimSpace(analysisHash) == "" || strings.TrimSpace(status) == "" {
		return nil
	}

	return &validatedProposalRecord{
		ApplicationID:      strings.TrimSpace(appID),
		EnvironmentID:      strings.TrimSpace(envID),
		ExpectedRevision:   uint64(expRevNum),
		ExpectedStateHash:  strings.TrimSpace(expStateHash),
		AnalysisInputsHash: strings.TrimSpace(analysisHash),
		DraftHash:          draftHash,
		Status:             status,
	}
}

func extractSourcePatchValidationRecordFromCall(rec *mcpCallRecord) *validatedSourcePatchRecord {
	args := rec.arguments
	if args == nil {
		return nil
	}
	proposal, _ := args["proposal"].(map[string]any)
	if proposal == nil {
		return nil
	}
	provenance, _ := proposal["provenance"].(map[string]any)

	status := ""
	proposalHash := ""
	if rec.result != nil {
		status, _ = rec.result["status"].(string)
		proposalHash, _ = rec.result["source_patch_proposal_hash"].(string)
	}
	if (status == "" || proposalHash == "") && rec.content != nil {
		if text := extractTextFromContent(rec.content); text != "" {
			var res map[string]any
			if json.Unmarshal([]byte(text), &res) == nil {
				if s, ok := res["status"].(string); ok {
					status = s
				}
				if h, ok := res["source_patch_proposal_hash"].(string); ok {
					proposalHash = h
				}
			}
		}
	}

	projectID, _ := proposal["project_id"].(string)
	environmentID, _ := proposal["environment_id"].(string)
	applicationID, _ := proposal["application_id"].(string)
	var sourceCommit, applicationRoot string
	if provenance != nil {
		sourceCommit, _ = provenance["source_commit"].(string)
		applicationRoot, _ = provenance["application_root"].(string)
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(environmentID) == "" || strings.TrimSpace(applicationID) == "" || strings.TrimSpace(sourceCommit) == "" || strings.TrimSpace(proposalHash) == "" || status == "" {
		return nil
	}
	raw, err := json.Marshal(proposal)
	if err != nil {
		return nil
	}
	return &validatedSourcePatchRecord{
		ProjectID:       strings.TrimSpace(projectID),
		EnvironmentID:   strings.TrimSpace(environmentID),
		ApplicationID:   strings.TrimSpace(applicationID),
		SourceCommit:    strings.TrimSpace(sourceCommit),
		ApplicationRoot: strings.TrimSpace(applicationRoot),
		ProposalHash:    strings.TrimSpace(proposalHash),
		Status:          status,
		ProposalJSON:    raw,
	}
}
