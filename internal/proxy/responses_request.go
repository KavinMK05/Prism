package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

func translateResponsesAPIToChatCompletions(req *ResponsesAPIRequest) *OpenAIChatRequest {
	messages := []OpenAIChatMessage{}

	// Handle instructions -> system message
	if req.Instructions != nil {
		instructions := instructionsToString(req.Instructions)
		if instructions != "" {
			messages = append(messages, OpenAIChatMessage{
				Role:    "system",
				Content: instructions,
			})
		}
	}

	// Handle input
	if req.Input != nil {
		switch input := req.Input.(type) {
		case string:
			if input != "" {
				messages = append(messages, OpenAIChatMessage{
					Role:    "user",
					Content: input,
				})
			}
		case []interface{}:
			messages = append(messages, translateResponsesInputToChatMessages(input)...)
		}
	}

	chatReq := &OpenAIChatRequest{
		Model:             req.Model,
		Messages:          messages,
		Stream:            req.Stream,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
	}

	if req.MaxOutputTokens > 0 {
		chatReq.MaxTokens = req.MaxOutputTokens
	}

	// Handle tools (merge top-level tools with any "additional_tools" input
	// items; Codex Desktop "Responses Lite" declares tools inside the input
	// array instead of the top-level field). Without this merge, those tools
	// never reach the upstream and the model cannot emit tool calls.
	allTools := collectAllResponseTools(req)
	if len(allTools) > 0 {
		chatReq.Tools = translateResponseToolsToChatCompletions(allTools)
	}

	// Handle reasoning
	if req.Reasoning != nil {
		if effort, ok := req.Reasoning.(string); ok {
			// Normalize non-standard effort values
			switch effort {
			case "enabled", "on", "true":
				chatReq.ReasoningEffort = "medium"
			case "disabled", "off", "false", "none":
				// Don't set reasoning_effort at all
			default:
				chatReq.ReasoningEffort = effort
			}
		} else if m, ok := req.Reasoning.(map[string]interface{}); ok {
			if e, ok := m["effort"].(string); ok {
				switch e {
				case "enabled", "on", "true":
					chatReq.ReasoningEffort = "medium"
				case "disabled", "off", "false", "none":
					// Don't set reasoning_effort
				default:
					chatReq.ReasoningEffort = e
				}
			}
		}
	}

	// Handle text.format -> response_format
	if req.Text != nil {
		chatReq.ResponseFormat = translateTextFormatToResponseFormat(req.Text)
	}

	return chatReq
}

// collectAllResponseTools merges the top-level tools array with any
// "additional_tools" input items (used by Codex Desktop "Responses Lite" to
// declare tools inside the input array instead of the top-level field).
// Returns a single combined slice preserving order: top-level tools first,
// then additional_tools items in input order.
func collectAllResponseTools(req *ResponsesAPIRequest) []interface{} {
	tools := []interface{}{}
	if len(req.Tools) > 0 {
		tools = append(tools, req.Tools...)
	}
	if req.Input != nil {
		if input, ok := req.Input.([]interface{}); ok {
			for _, item := range input {
				m, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				if t, _ := m["type"].(string); t == "additional_tools" {
					if at, ok := m["tools"].([]interface{}); ok {
						tools = append(tools, at...)
					}
				}
			}
		}
	}
	return tools
}

// translateResponsesInputToChatMessages converts the Responses API input
// array into a Chat Completions messages array. It buffers consecutive
// function_call / custom_tool_call items into a single assistant message
// (providers require parallel tool calls in one assistant message) and keeps
// assistant(tool_calls) -> tool(output) adjacency strict by deferring any
// interleaved non-tool messages until all pending tool outputs arrive.
// Mirrors the buffering strategy used by the CLIProxyAPI reference
// implementation.
func translateResponsesInputToChatMessages(input []interface{}) []OpenAIChatMessage {
	// Pair tool outputs with their calls first: Codex does not always populate
	// `call_id` on output items (see normalizeResponsesToolCallOutputs) and every
	// branch below depends on it being present.
	input = normalizeResponsesToolCallOutputs(input)
	callIDToName := buildResponsesCallIDToNameMap(input)

	// First pass: collect call_ids that have outputs in this input, so we can
	// tell which pending tool calls are still awaiting outputs (and therefore
	// must keep any interleaved messages deferred to preserve adjacency).
	outputCallIDs := map[string]struct{}{}
	for _, item := range input {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if t != "function_call_output" && t != "custom_tool_call_output" {
			continue
		}
		callID := extractResponsesCallID(m)
		if callID != "" {
			outputCallIDs[callID] = struct{}{}
		}
	}

	var messages []OpenAIChatMessage
	var pendingToolCalls []OpenAIToolCall
	var pendingToolCallIDs []string
	pendingReasoningContent := ""
	awaitingToolOutputs := map[string]struct{}{}
	var deferredMessages []OpenAIChatMessage

	takePendingReasoning := func() string {
		rc := pendingReasoningContent
		pendingReasoningContent = ""
		return rc
	}
	flushPendingToolCalls := func() {
		if len(pendingToolCalls) == 0 {
			return
		}
		msg := OpenAIChatMessage{
			Role:      "assistant",
			ToolCalls: pendingToolCalls,
		}
		if rc := takePendingReasoning(); rc != "" {
			msg.ReasoningContent = &rc
		}
		messages = append(messages, msg)
		for _, id := range pendingToolCallIDs {
			if strings.TrimSpace(id) != "" {
				awaitingToolOutputs[id] = struct{}{}
			}
		}
		pendingToolCalls = nil
		pendingToolCallIDs = nil
	}
	flushDeferred := func() {
		messages = append(messages, deferredMessages...)
		deferredMessages = nil
	}
	hasAwaitingOutput := func() bool {
		for id := range awaitingToolOutputs {
			if _, ok := outputCallIDs[id]; ok {
				return true
			}
		}
		return false
	}
	appendRegular := func(msg OpenAIChatMessage) {
		// Keep tool-call adjacency strict for providers that require
		// assistant(tool_calls) -> tool(tool_call_id) with no message in between.
		if hasAwaitingOutput() {
			deferredMessages = append(deferredMessages, msg)
			return
		}
		messages = append(messages, msg)
	}
	appendPendingReasoningMessage := func() {
		rc := takePendingReasoning()
		if rc == "" {
			return
		}
		appendRegular(OpenAIChatMessage{Role: "assistant", Content: "", ReasoningContent: &rc})
	}

	for _, item := range input {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		if itemType == "" {
			if _, hasRole := m["role"]; hasRole {
				itemType = "message"
			}
		}
		// Anything that is not a tool call flushes buffered tool calls first so
		// consecutive function_call/custom_tool_call items group together.
		if itemType != "function_call" && itemType != "custom_tool_call" {
			flushPendingToolCalls()
		}

		switch itemType {
		case "message", "":
			role, _ := m["role"].(string)
			if role == "developer" {
				role = "user"
			}
			if role != "assistant" {
				appendPendingReasoningMessage()
			}
			msg := buildResponsesMessageToChat(m, role)
			if role == "assistant" {
				// Attach reasoning: prefer the item's own, else pending.
				rc := getMapString(m, "reasoning_content")
				if rc == "" {
					rc = takePendingReasoning()
				} else {
					pendingReasoningContent = ""
				}
				if rc != "" {
					msg.ReasoningContent = &rc
				}
			}
			appendRegular(msg)

		case "reasoning":
			rc := responsesReasoningSummaryText(item)
			if pendingReasoningContent == "" {
				pendingReasoningContent = rc
			} else {
				pendingReasoningContent += rc
			}

		case "function_call":
			callID := extractResponsesCallID(m)
			name := replayToolName(m)
			arguments := getMapString(m, "arguments")
			pendingToolCalls = append(pendingToolCalls, OpenAIToolCall{
				ID:   callID,
				Type: "function",
				Function: OpenAIToolCallFunc{
					Name:      name,
					Arguments: arguments,
				},
			})
			if cid := strings.TrimSpace(callID); cid != "" {
				pendingToolCallIDs = append(pendingToolCallIDs, cid)
			}

		case "custom_tool_call":
			// Codex freeform tool call replay: wrap the raw input so it matches
			// the {"input": string} function shape used when converting custom
			// tool definitions for the chat-completions upstream.
			callID := extractResponsesCallID(m)
			name := replayToolName(m)
			inputVal := getMapString(m, "input")
			wrapped, _ := json.Marshal(map[string]interface{}{"input": inputVal})
			pendingToolCalls = append(pendingToolCalls, OpenAIToolCall{
				ID:   callID,
				Type: "function",
				Function: OpenAIToolCallFunc{
					Name:      name,
					Arguments: string(wrapped),
				},
			})
			if cid := strings.TrimSpace(callID); cid != "" {
				pendingToolCallIDs = append(pendingToolCallIDs, cid)
			}

		case "function_call_output":
			callID := extractResponsesCallID(m)
			// Tool outputs may carry image parts (Codex's view_image); keep them.
			content := responsesToolOutputChatContent(m["output"])
			if _, awaiting := awaitingToolOutputs[callID]; callID == "" || !awaiting {
				// Orphan output: no tool call with this id was sent upstream (or the
				// id is unusable). A role:"tool" message with an empty or unknown
				// tool_call_id is rejected by OpenAI-compatible upstreams and makes
				// the model repeat the same call, so surface the text as a user
				// message instead — matching the CLIProxyAPI reference.
				appendRegular(OpenAIChatMessage{Role: "user", Content: content})
				continue
			}
			msg := OpenAIChatMessage{Role: "tool", ToolID: callID, Content: content}
			if name, ok := callIDToName[callID]; ok {
				msg.Name = name
			}
			// Tool outputs emit directly (never deferred) so they immediately
			// follow the assistant(tool_calls) message.
			messages = append(messages, msg)
			delete(awaitingToolOutputs, callID)
			if len(awaitingToolOutputs) == 0 && len(deferredMessages) > 0 {
				flushDeferred()
			}

		case "custom_tool_call_output":
			callID := extractResponsesCallID(m)
			content := responsesToolOutputChatContent(m["output"])
			if _, awaiting := awaitingToolOutputs[callID]; callID == "" || !awaiting {
				// Orphan output (see the function_call_output case above).
				appendRegular(OpenAIChatMessage{Role: "user", Content: content})
				continue
			}
			msg := OpenAIChatMessage{Role: "tool", ToolID: callID, Content: content}
			if name, ok := callIDToName[callID]; ok {
				msg.Name = name
			}
			messages = append(messages, msg)
			delete(awaitingToolOutputs, callID)
			if len(awaitingToolOutputs) == 0 && len(deferredMessages) > 0 {
				flushDeferred()
			}

		default:
			// Unrecognized type with a role -> role-based fallback.
			role, _ := m["role"].(string)
			if role == "" {
				continue
			}
			if role == "developer" {
				role = "user"
			}
			appendRegular(OpenAIChatMessage{Role: role, Content: getMapString(m, "content")})
		}
	}
	flushPendingToolCalls()
	appendPendingReasoningMessage()
	flushDeferred()
	return messages
}

// buildResponsesMessageToChat builds a Chat Completions message from a
// Responses API "message" item, handling string content, structured content
// arrays (text/input_text/input_image/input_file/image_url), and image
// passthrough.
func buildResponsesMessageToChat(m map[string]interface{}, role string) OpenAIChatMessage {
	c, hasContent := m["content"]
	if !hasContent || c == nil {
		return OpenAIChatMessage{Role: role, Content: ""}
	}
	if contentStr, ok := c.(string); ok {
		return OpenAIChatMessage{Role: role, Content: contentStr}
	}
	contentArray, ok := c.([]interface{})
	if !ok {
		b, _ := json.Marshal(c)
		return OpenAIChatMessage{Role: role, Content: string(b)}
	}

	// Scan for image/file content parts to decide between string and structured content.
	hasMedia := false
	for _, part := range contentArray {
		if pMap, ok := part.(map[string]interface{}); ok {
			pType, _ := pMap["type"].(string)
			if pType == "input_image" || pType == "input_file" || pType == "image_url" {
				hasMedia = true
				break
			}
		}
	}

	if hasMedia {
		contentParts := []interface{}{}
		for _, part := range contentArray {
			if s, ok := part.(string); ok {
				contentParts = append(contentParts, map[string]interface{}{
					"type": "text",
					"text": s,
				})
				continue
			}
			pMap, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			pType, _ := pMap["type"].(string)
			switch pType {
			case "text", "input_text", "output_text":
				if t, ok := pMap["text"].(string); ok {
					contentParts = append(contentParts, map[string]interface{}{
						"type": "text",
						"text": t,
					})
				}
			case "input_image":
				imageURL, _ := pMap["image_url"].(string)
				if imageURL != "" {
					imageURLObj := map[string]interface{}{"url": imageURL}
					if detail, ok := pMap["detail"].(string); ok && detail != "" {
						imageURLObj["detail"] = detail
					}
					contentParts = append(contentParts, map[string]interface{}{
						"type":      "image_url",
						"image_url": imageURLObj,
					})
				}
			case "input_file":
				if fileData, ok := pMap["file_data"].(string); ok && fileData != "" {
					contentParts = append(contentParts, map[string]interface{}{
						"type":      "image_url",
						"image_url": map[string]interface{}{"url": fileData},
					})
				} else if fileURL, ok := pMap["file_url"].(string); ok && fileURL != "" {
					contentParts = append(contentParts, map[string]interface{}{
						"type":      "image_url",
						"image_url": map[string]interface{}{"url": fileURL},
					})
				}
			case "image_url":
				contentParts = append(contentParts, pMap)
			}
		}
		return OpenAIChatMessage{Role: role, Content: contentParts}
	}

	// No media — flatten to string.
	parts := []string{}
	for _, part := range contentArray {
		if s, ok := part.(string); ok {
			parts = append(parts, s)
			continue
		}
		if pMap, ok := part.(map[string]interface{}); ok {
			if pType, _ := pMap["type"].(string); pType == "text" || pType == "input_text" || pType == "output_text" {
				if t, ok := pMap["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
	}
	return OpenAIChatMessage{Role: role, Content: strings.Join(parts, "")}
}

// responsesToolOutputText flattens a tool output value that may be a plain
// string or an array of content parts ({"type":"...","text":...}) into a
// single text payload for a Chat Completions tool message.
func responsesToolOutputText(output interface{}) string {
	if output == nil {
		return ""
	}
	if s, ok := output.(string); ok {
		return s
	}
	if arr, ok := output.([]interface{}); ok {
		var sb strings.Builder
		for _, part := range arr {
			if s, ok := part.(string); ok {
				sb.WriteString(s)
				continue
			}
			if pm, ok := part.(map[string]interface{}); ok {
				if t, ok := pm["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	b, _ := json.Marshal(output)
	return string(b)
}

// responsesToolOutputParts splits a Responses tool-output payload into the text
// the model can read and the images it should see.
//
// A tool output is either a plain string or an array of content parts. Codex's
// view_image returns image parts (input_image / input_file), and those used to be
// dropped entirely: only `text` fields were read, so the model received an empty
// tool result and reported that the image "delivered nothing". Images are
// returned as data URLs so each provider can take the shape it needs (Ollama
// wants bare base64 in `images`, chat-completions wants the URL intact).
func responsesToolOutputParts(output interface{}) (string, []string) {
	arr, ok := output.([]interface{})
	if !ok {
		return responsesToolOutputText(output), nil
	}

	var sb strings.Builder
	var images []string
	for _, part := range arr {
		if s, ok := part.(string); ok {
			sb.WriteString(s)
			continue
		}
		pm, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		switch getMapString(pm, "type") {
		case "text", "input_text", "output_text":
			sb.WriteString(getMapString(pm, "text"))
		case "input_image", "image_url":
			if url := responsesImageURL(pm); url != "" {
				images = append(images, url)
			}
		case "input_file":
			if data := getMapString(pm, "file_data"); strings.HasPrefix(data, "data:") {
				images = append(images, data)
			}
		default:
			// Unknown part types may still carry readable text.
			sb.WriteString(getMapString(pm, "text"))
		}
	}

	text := sb.String()
	if len(images) > 0 && strings.TrimSpace(text) == "" {
		// Never hand the model an empty tool result: say what came back.
		text = fmt.Sprintf("[tool output contained %d image(s); see the attached image data]", len(images))
	}
	return text, images
}

// responsesImageURL accepts both the string and the object form of an image part
// ("image_url": "data:…" and "image_url": {"url": "data:…"}).
func responsesImageURL(part map[string]interface{}) string {
	if s, ok := part["image_url"].(string); ok {
		return s
	}
	if m, ok := part["image_url"].(map[string]interface{}); ok {
		return getMapString(m, "url")
	}
	return ""
}

// responsesToolOutputChatContent builds the chat-completions `content` value for
// a tool output: a plain string when the output is text-only, or a multimodal
// parts array when the tool returned images. Mirrors CLIProxyAPI's
// setFunctionCallOutputContent, which switches to a content array only when an
// image part is present so text-only outputs keep the plain-string shape.
func responsesToolOutputChatContent(output interface{}) interface{} {
	text, images := responsesToolOutputParts(output)
	if len(images) == 0 {
		return text
	}
	parts := []interface{}{}
	if text != "" {
		parts = append(parts, map[string]interface{}{"type": "text", "text": text})
	}
	for _, url := range images {
		parts = append(parts, map[string]interface{}{
			"type":      "image_url",
			"image_url": map[string]interface{}{"url": url},
		})
	}
	return parts
}

// ollamaToolOutputImages converts image data URLs into the bare base64 payloads
// Ollama's `images` field expects. HTTP(S) URLs are skipped with a warning, the
// same as user-message images on the Ollama path.
func ollamaToolOutputImages(images []string) []string {
	if len(images) == 0 {
		return nil
	}
	out := make([]string, 0, len(images))
	for _, url := range images {
		if !strings.HasPrefix(url, "data:") {
			log.Printf("[WARN] tool output image with HTTP URL not supported for Ollama provider, skipping: %s", url)
			continue
		}
		if parts := strings.SplitN(url, ",", 2); len(parts) == 2 {
			out = append(out, parts[1])
		}
	}
	return out
}

// getMapString returns m[key] as a string, or "" when absent / non-string.
func getMapString(m map[string]interface{}, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// buildResponsesCallIDToNameMap builds a mapping from call_id to function name
// by scanning tool-call items in the input array. Covers all Responses API
// tool-call output item types (function_call, custom_tool_call, web_search_call,
// local_shell_call, computer_call) so the result of a built-in tool (sent back
// as function_call_output) can be matched to its function name regardless of
// the original tool type.
func buildResponsesCallIDToNameMap(items []interface{}) map[string]string {
	idToName := map[string]string{}
	for _, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		switch itemType {
		case "function_call", "custom_tool_call", "web_search_call",
			"local_shell_call", "computer_call":
			callID := extractResponsesCallID(itemMap)
			// The upstream tool list declares namespaced tools in their
			// qualified form ("ns__child"), so the tool message has to carry the
			// same name the assistant tool_call used.
			name := replayToolName(itemMap)
			if callID != "" && name != "" {
				idToName[callID] = name
			}
		}
	}
	return idToName
}

// extractResponsesCallID resolves the call id of a Responses tool-call or
// tool-output item. Codex does not reliably populate `call_id`: some builds send
// only the output-item id ("fco_…", which is an item id rather than a call id),
// a camelCase `callId`, or a `tool_call_id`. Mirrors CLIProxyAPI's
// ExtractResponsesCallID, including ignoring fco_* output-item ids.
func extractResponsesCallID(item map[string]interface{}) string {
	for _, key := range []string{"call_id", "tool_call_id", "callId"} {
		if v := strings.TrimSpace(getMapString(item, key)); v != "" {
			return v
		}
	}
	id := strings.TrimSpace(getMapString(item, "id"))
	if strings.HasPrefix(id, "fco_") {
		// An output-item id can never match a tool call.
		return ""
	}
	return id
}

// replayToolName returns the upstream function name for a replayed tool-call
// item. Codex splits a namespaced call into `name` + `namespace` on the response
// item, but the upstream tool list only declares the qualified form
// ("ns__child"), so the replayed assistant tool_call must be re-qualified or the
// upstream rejects it as an unknown function. Mirrors the
// qualifyResponsesNamespaceToolName call on CLIProxyAPI's request path.
func replayToolName(item map[string]interface{}) string {
	name := strings.TrimSpace(getMapString(item, "name"))
	namespace := strings.TrimSpace(getMapString(item, "namespace"))
	if name == "" || namespace == "" {
		return name
	}
	return qualifyResponsesNamespaceToolName(namespace, name)
}

// isResponsesToolOutput reports whether an input item carries the result of a
// tool call.
func isResponsesToolOutput(item map[string]interface{}) bool {
	switch getMapString(item, "type") {
	case "function_call_output", "custom_tool_call_output":
		return true
	}
	return false
}

// normalizeResponsesToolCallOutputs pairs each tool output with the tool call it
// belongs to and fills in a `call_id` when the client omitted it.
//
// Codex does not always populate `call_id` on output items — newer builds and the
// "Responses Lite" payloads may carry only the output-item id ("fco_…"), a
// camelCase `callId`, or nothing at all. Unpaired outputs used to be replayed as
// role:"tool" messages with an empty tool_call_id, which OpenAI-compatible
// upstreams reject and which makes the model re-issue the same tool call
// indefinitely.
//
// Matching is the three-pass strategy used by the CLIProxyAPI reference:
//
//  1. exact call id
//  2. function name carried on the output item
//  3. FIFO order
//
// Passes 2 and 3 skip a pending call while a later output still references it
// explicitly, so an id-less output cannot steal a call that an explicit output
// needs. Outputs that already carry a call id matching no pending call are left
// untouched; the translators surface those as user text.
//
// The input slice and its item maps are never mutated, because the request is
// echoed back to the client and dumped to the debug capture.
func normalizeResponsesToolCallOutputs(input []interface{}) []interface{} {
	if len(input) == 0 {
		return input
	}
	items := make([]interface{}, len(input))
	copy(items, input)

	// Count explicit output references up front so a later explicit output can
	// reserve its call against the id-less matching passes.
	reserved := map[string]int{}
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok || !isResponsesToolOutput(m) {
			continue
		}
		if id := extractResponsesCallID(m); id != "" {
			reserved[id]++
		}
	}

	var pendingIDs []string
	pendingNames := map[string]string{}

	for i := 0; i < len(items); i++ {
		m, ok := items[i].(map[string]interface{})
		if !ok {
			continue
		}
		switch getMapString(m, "type") {
		case "function_call", "custom_tool_call":
			id := extractResponsesCallID(m)
			if id == "" {
				break
			}
			pendingIDs = append(pendingIDs, id)
			pendingNames[id] = replayToolName(m)

		case "function_call_output", "custom_tool_call_output":
			start := i
			for i < len(items) {
				out, ok := items[i].(map[string]interface{})
				if !ok || !isResponsesToolOutput(out) {
					break
				}
				i++
			}
			matched := matchResponsesToolOutputs(pendingIDs, pendingNames, items[start:i], reserved)
			var unmatched []string
			for idx, id := range pendingIDs {
				if matched[idx] < 0 {
					unmatched = append(unmatched, id)
					continue
				}
				out := items[start+matched[idx]].(map[string]interface{})
				if extractResponsesCallID(out) == id {
					continue
				}
				withID := make(map[string]interface{}, len(out)+1)
				for k, v := range out {
					withID[k] = v
				}
				withID["call_id"] = id
				items[start+matched[idx]] = withID
			}
			pendingIDs = unmatched
			i-- // the run already advanced past the outputs
		}
	}
	return items
}

// matchResponsesToolOutputs returns, for each pending call (same index), the
// index of the output item it should be paired with, or -1 when unmatched.
func matchResponsesToolOutputs(pendingIDs []string, pendingNames map[string]string, outputs []interface{}, reserved map[string]int) []int {
	matched := make([]int, len(pendingIDs))
	for i := range matched {
		matched[i] = -1
	}
	used := make([]bool, len(outputs))

	// Pass 1: exact call id.
	for p, id := range pendingIDs {
		for o, item := range outputs {
			out, ok := item.(map[string]interface{})
			if !ok || used[o] {
				continue
			}
			if extractResponsesCallID(out) == id {
				used[o] = true
				matched[p] = o
				reserved[id]--
				break
			}
		}
	}

	// Pass 2: the output item names the function it belongs to.
	for p, id := range pendingIDs {
		if matched[p] >= 0 || reserved[id] > 0 {
			continue
		}
		expected := pendingNames[id]
		if expected == "" {
			continue
		}
		for o, item := range outputs {
			out, ok := item.(map[string]interface{})
			if !ok || used[o] || extractResponsesCallID(out) != "" {
				continue
			}
			if name := strings.TrimSpace(getMapString(out, "name")); name != "" && name == expected {
				used[o] = true
				matched[p] = o
				break
			}
		}
	}

	// Pass 3: FIFO order.
	for p, id := range pendingIDs {
		if matched[p] >= 0 || reserved[id] > 0 {
			continue
		}
		for o, item := range outputs {
			out, ok := item.(map[string]interface{})
			if !ok || used[o] || extractResponsesCallID(out) != "" {
				continue
			}
			if name := strings.TrimSpace(getMapString(out, "name")); name != "" && pendingNames[id] != "" && name != pendingNames[id] {
				continue
			}
			used[o] = true
			matched[p] = o
			break
		}
	}
	return matched
}

// responsesReasoningSummaryText returns the concatenated summary_text from a
// "reasoning" input item, or "" if the item is not a reasoning item or has no
// summary text. Used to buffer reasoning content and attach it to the next
// assistant message.
func responsesReasoningSummaryText(item interface{}) string {
	itemMap, ok := item.(map[string]interface{})
	if !ok {
		return ""
	}
	if t, _ := itemMap["type"].(string); t != "reasoning" {
		return ""
	}
	var sb strings.Builder
	if summary, ok := itemMap["summary"].([]interface{}); ok {
		for _, s := range summary {
			m, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t != "" && t != "summary_text" {
				continue
			}
			if text, ok := m["text"].(string); ok {
				sb.WriteString(text)
			}
		}
	}
	return sb.String()
}

func translateResponseToolsToChatCompletions(tools []interface{}) []OpenAITool {
	result := []OpenAITool{}
	for _, tool := range tools {
		result = append(result, convertResponsesToolToOpenAIChatTools(tool)...)
	}
	return result
}

// convertResponsesToolToOpenAIChatTools converts a single Responses API tool
// definition into one or more Chat Completions function tools. "function" and
// freeform "custom" tools map to a single function tool; "namespace" (MCP)
// tools expand into one function tool per qualified child; Codex built-in
// tool types (apply_patch, web_search, local_shell, ...) are rewrapped as a
// function tool named after the built-in so the upstream chat-completions
// model can invoke them.
func convertResponsesToolToOpenAIChatTools(tool interface{}) []OpenAITool {
	toolMap, ok := tool.(map[string]interface{})
	if !ok {
		return nil
	}
	toolType := strings.TrimSpace(getMapString(toolMap, "type"))
	switch toolType {
	case "", "function":
		if t, ok := convertResponsesFunctionToolToOpenAIChat(toolMap, ""); ok {
			return []OpenAITool{t}
		}
	case "custom":
		if t, ok := convertResponsesCustomToolToOpenAIChat(toolMap, ""); ok {
			return []OpenAITool{t}
		}
	case "namespace":
		return convertResponsesNamespaceToolToOpenAIChat(toolMap)
	default:
		return []OpenAITool{convertResponsesBuiltinToolToOpenAIChat(toolMap, toolType)}
	}
	return nil
}

func convertResponsesFunctionToolToOpenAIChat(toolMap map[string]interface{}, overrideName string) (OpenAITool, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(toolMap)
	}
	if name == "" {
		return OpenAITool{}, false
	}
	parameters := responsesToolParameters(toolMap)
	if parameters == nil {
		parameters = map[string]interface{}{"type": "object"}
	}
	return OpenAITool{
		Type: "function",
		Function: OpenAIToolDef{
			Name:        name,
			Description: responsesToolDescription(toolMap),
			Parameters:  parameters,
		},
	}, true
}

// convertResponsesCustomToolToOpenAIChat maps a Responses freeform ("custom")
// tool onto a Chat Completions function tool with a single freeform "input"
// string, mirroring the function-based shape Codex uses for apply_patch.
func convertResponsesCustomToolToOpenAIChat(toolMap map[string]interface{}, overrideName string) (OpenAITool, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(toolMap)
	}
	if name == "" {
		return OpenAITool{}, false
	}
	return OpenAITool{
		Type: "function",
		Function: OpenAIToolDef{
			Name:        name,
			Description: responsesCustomToolDescriptionWithFormat(toolMap),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{"type": "string"},
				},
				"required": []string{"input"},
			},
		},
	}, true
}

func convertResponsesNamespaceToolToOpenAIChat(toolMap map[string]interface{}) []OpenAITool {
	namespaceName := strings.TrimSpace(getMapString(toolMap, "name"))
	children, ok := toolMap["tools"].([]interface{})
	if !ok {
		return nil
	}
	var out []OpenAITool
	for _, child := range children {
		childMap, ok := child.(map[string]interface{})
		if !ok {
			continue
		}
		childName := responsesToolName(childMap)
		qualifiedName := qualifyResponsesNamespaceToolName(namespaceName, childName)
		switch strings.TrimSpace(getMapString(childMap, "type")) {
		case "", "function":
			if t, ok := convertResponsesFunctionToolToOpenAIChat(childMap, qualifiedName); ok {
				out = append(out, t)
			}
		case "custom":
			if t, ok := convertResponsesCustomToolToOpenAIChat(childMap, qualifiedName); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// convertResponsesBuiltinToolToOpenAIChat rewraps a Codex built-in / native
// tool (apply_patch, local_shell, web_search, computer_use, mcp__*, ...) as a
// Chat Completions function tool, synthesizing a name/description/parameters
// when the request did not supply them.
func convertResponsesBuiltinToolToOpenAIChat(toolMap map[string]interface{}, toolType string) OpenAITool {
	normalizedType := strings.ToLower(strings.TrimSpace(toolType))
	name := getMapString(toolMap, "name")
	if name == "" {
		name = nativeToolFunctionName(normalizedType)
	}
	name = sanitizeToolName(name)
	description := getMapString(toolMap, "description")
	if description == "" {
		description = nativeToolDescription(normalizedType)
	}
	parameters := toolMap["parameters"]
	if parameters == nil {
		parameters = nativeToolParameters(normalizedType)
	}
	return OpenAITool{
		Type: "function",
		Function: OpenAIToolDef{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}
}

func responsesToolName(toolMap map[string]interface{}) string {
	if name := strings.TrimSpace(getMapString(toolMap, "name")); name != "" {
		return name
	}
	if fn, ok := toolMap["function"].(map[string]interface{}); ok {
		if name := strings.TrimSpace(getMapString(fn, "name")); name != "" {
			return name
		}
	}
	return ""
}

func responsesToolDescription(toolMap map[string]interface{}) string {
	if d := getMapString(toolMap, "description"); d != "" {
		return d
	}
	if fn, ok := toolMap["function"].(map[string]interface{}); ok {
		return getMapString(fn, "description")
	}
	return ""
}

func responsesToolParameters(toolMap map[string]interface{}) interface{} {
	for _, key := range []string{"parameters", "parametersJsonSchema", "input_schema"} {
		if v, ok := toolMap[key]; ok && v != nil {
			return v
		}
	}
	if fn, ok := toolMap["function"].(map[string]interface{}); ok {
		for _, key := range []string{"parameters", "parametersJsonSchema"} {
			if v, ok := fn[key]; ok && v != nil {
				return v
			}
		}
	}
	return nil
}

// responsesCustomToolDescriptionWithFormat returns the custom tool description
// augmented with the tool's `format` grammar when present. Responses-API
// freeform tools (e.g. apply_patch) carry a lark grammar in `format` that
// describes the exact text the `input` field must contain. Neither Ollama nor
// Chat Completions understand a `format` field, so without this the model only
// sees a vague "do not wrap the patch in JSON" hint and guesses the wrong patch
// syntax. Embedding the grammar definition into the description preserves that
// contract.
func responsesCustomToolDescriptionWithFormat(toolMap map[string]interface{}) string {
	desc := responsesToolDescription(toolMap)
	formatRaw, ok := toolMap["format"]
	if !ok || formatRaw == nil {
		return desc
	}
	formatMap, ok := formatRaw.(map[string]interface{})
	if !ok {
		return desc
	}
	definition := strings.TrimSpace(getMapString(formatMap, "definition"))
	if definition == "" {
		return desc
	}
	// Normalize CRLF line endings from the grammar definition for readability.
	definition = strings.ReplaceAll(definition, "\r\n", "\n")
	var b strings.Builder
	b.WriteString(desc)
	if !strings.HasSuffix(desc, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\nThe `input` string must conform to the following grammar (do not deviate from it):\n")
	if syntax := strings.TrimSpace(getMapString(formatMap, "syntax")); syntax != "" {
		b.WriteString("(syntax: ")
		b.WriteString(syntax)
		b.WriteString(")\n")
	}
	b.WriteString(definition)
	if !strings.HasSuffix(definition, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// qualifyResponsesNamespaceToolName builds the Chat Completions function name
// for a namespace (MCP) child tool: "namespace__child" (or "mcp__..." when the
// child already carries that prefix). Mirrors the CLIProxyAPI reference.
func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if strings.HasPrefix(childName, namespaceName) {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func instructionsToString(instructions interface{}) string {
	switch v := instructions.(type) {
	case string:
		return v
	case []interface{}:
		parts := []string{}
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if instructions != nil {
			return fmt.Sprintf("%v", instructions)
		}
	}
	return ""
}

func translateTextFormatToResponseFormat(text interface{}) interface{} {
	textMap, ok := text.(map[string]interface{})
	if !ok {
		return nil
	}
	if format, ok := textMap["format"]; ok {
		return format
	}
	return nil
}

func translateResponsesAPIToOllama(req *ResponsesAPIRequest) *OllamaChatRequest {
	messages := []OllamaMessage{}

	// Handle instructions -> system message
	if req.Instructions != nil {
		instructions := instructionsToString(req.Instructions)
		if instructions != "" {
			messages = append(messages, OllamaMessage{
				Role:    "system",
				Content: instructions,
			})
		}
	}

	// Handle input
	if req.Input != nil {
		switch input := req.Input.(type) {
		case string:
			if input != "" {
				messages = append(messages, OllamaMessage{
					Role:    "user",
					Content: input,
				})
			}
		case []interface{}:
			messages = append(messages, translateResponsesInputToOllamaMessages(input)...)
		}
	}

	ollamaReq := &OllamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
	}

	// Handle tools (Ollama only understands function tools). Merge top-level
	// tools with any "additional_tools" input items.
	allTools := collectAllResponseTools(req)
	if len(allTools) > 0 {
		ollamaReq.Tools = translateResponseToolsToOllama(allTools)
	}

	// Handle options
	options := &OllamaOptions{}
	hasOptions := false

	if req.MaxOutputTokens > 0 {
		options.NumPredict = req.MaxOutputTokens
		hasOptions = true
	}
	if req.Temperature != nil {
		options.Temperature = req.Temperature
		hasOptions = true
	}
	if req.TopP != nil {
		options.TopP = req.TopP
		hasOptions = true
	}

	if hasOptions {
		ollamaReq.Options = options
	}

	// Handle reasoning -> think
	if req.Reasoning != nil {
		ollamaReq.Think = true
	}

	// Handle text.format
	if req.Text != nil {
		ollamaReq.Format = translateTextFormatToResponseFormat(req.Text)
	}

	return ollamaReq
}

// translateResponsesInputToOllamaMessages mirrors
// translateResponsesInputToChatMessages for the Ollama provider: consecutive
// function_call / custom_tool_call items are buffered into a single assistant
// message and tool-call -> tool adjacency is preserved.
func translateResponsesInputToOllamaMessages(input []interface{}) []OllamaMessage {
	// Same output/call pairing as the chat-completions translator.
	input = normalizeResponsesToolCallOutputs(input)
	callIDToName := buildResponsesCallIDToNameMap(input)

	outputCallIDs := map[string]struct{}{}
	for _, item := range input {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if t != "function_call_output" && t != "custom_tool_call_output" {
			continue
		}
		callID := extractResponsesCallID(m)
		if callID != "" {
			outputCallIDs[callID] = struct{}{}
		}
	}

	var messages []OllamaMessage
	var pendingToolCalls []OllamaToolCall
	var pendingToolCallIDs []string
	pendingReasoningContent := ""
	awaitingToolOutputs := map[string]struct{}{}
	var deferredMessages []OllamaMessage

	takePendingReasoning := func() string {
		rc := pendingReasoningContent
		pendingReasoningContent = ""
		return rc
	}
	flushPendingToolCalls := func() {
		if len(pendingToolCalls) == 0 {
			return
		}
		msg := OllamaMessage{Role: "assistant", ToolCalls: pendingToolCalls}
		if rc := takePendingReasoning(); rc != "" {
			msg.Thinking = rc
		}
		messages = append(messages, msg)
		for _, id := range pendingToolCallIDs {
			if strings.TrimSpace(id) != "" {
				awaitingToolOutputs[id] = struct{}{}
			}
		}
		pendingToolCalls = nil
		pendingToolCallIDs = nil
	}
	flushDeferred := func() {
		messages = append(messages, deferredMessages...)
		deferredMessages = nil
	}
	hasAwaitingOutput := func() bool {
		for id := range awaitingToolOutputs {
			if _, ok := outputCallIDs[id]; ok {
				return true
			}
		}
		return false
	}
	appendRegular := func(msg OllamaMessage) {
		if hasAwaitingOutput() {
			deferredMessages = append(deferredMessages, msg)
			return
		}
		messages = append(messages, msg)
	}
	appendPendingReasoningMessage := func() {
		rc := takePendingReasoning()
		if rc == "" {
			return
		}
		appendRegular(OllamaMessage{Role: "assistant", Content: "", Thinking: rc})
	}

	for _, item := range input {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		if itemType == "" {
			if _, hasRole := m["role"]; hasRole {
				itemType = "message"
			}
		}
		if itemType != "function_call" && itemType != "custom_tool_call" {
			flushPendingToolCalls()
		}

		switch itemType {
		case "message", "":
			role, _ := m["role"].(string)
			if role == "" {
				role = "user"
			}
			if role == "developer" {
				role = "user"
			}
			if role != "assistant" {
				appendPendingReasoningMessage()
			}
			msg := buildResponsesMessageToOllama(m, role)
			if role == "assistant" {
				rc := getMapString(m, "reasoning_content")
				if rc == "" {
					rc = takePendingReasoning()
				} else {
					pendingReasoningContent = ""
				}
				if rc != "" {
					msg.Thinking = rc
				}
			}
			appendRegular(msg)

		case "reasoning":
			rc := responsesReasoningSummaryText(item)
			if pendingReasoningContent == "" {
				pendingReasoningContent = rc
			} else {
				pendingReasoningContent += rc
			}

		case "function_call":
			callID := extractResponsesCallID(m)
			name := replayToolName(m)
			arguments := getMapString(m, "arguments")
			var args map[string]interface{}
			if arguments != "" {
				json.Unmarshal([]byte(arguments), &args)
			}
			if args == nil {
				args = map[string]interface{}{}
			}
			pendingToolCalls = append(pendingToolCalls, OllamaToolCall{
				ID: callID,
				Function: OllamaToolCallFunction{
					Name:      name,
					Arguments: args,
				},
			})
			if cid := strings.TrimSpace(callID); cid != "" {
				pendingToolCallIDs = append(pendingToolCallIDs, cid)
			}

		case "custom_tool_call":
			callID := extractResponsesCallID(m)
			name := replayToolName(m)
			inputVal := getMapString(m, "input")
			var args map[string]interface{}
			if inputVal != "" {
				args = map[string]interface{}{"input": inputVal}
			} else {
				args = map[string]interface{}{}
			}
			pendingToolCalls = append(pendingToolCalls, OllamaToolCall{
				ID: callID,
				Function: OllamaToolCallFunction{
					Name:      name,
					Arguments: args,
				},
			})
			if cid := strings.TrimSpace(callID); cid != "" {
				pendingToolCallIDs = append(pendingToolCallIDs, cid)
			}

		case "function_call_output", "custom_tool_call_output":
			callID := extractResponsesCallID(m)
			// Tool outputs may carry image parts (Codex's view_image): Ollama takes
			// them as bare base64 in `images` on the same tool message.
			output, imageURLs := responsesToolOutputParts(m["output"])
			images := ollamaToolOutputImages(imageURLs)
			if _, awaiting := awaitingToolOutputs[callID]; callID == "" || !awaiting {
				// Orphan output: a role:"tool" message with no usable tool_call_id
				// (or one no tool call declared) leaves the upstream unable to
				// correlate the result, so the model repeats the same call. Emit the
				// text as a user message instead — matching the CLIProxyAPI
				// reference.
				appendRegular(OllamaMessage{Role: "user", Content: output, Images: images})
				continue
			}
			msg := OllamaMessage{Role: "tool", Content: output, Images: images, ToolCallID: callID}
			if name, ok := callIDToName[callID]; ok {
				msg.ToolName = name
			}
			messages = append(messages, msg)
			delete(awaitingToolOutputs, callID)
			if len(awaitingToolOutputs) == 0 && len(deferredMessages) > 0 {
				flushDeferred()
			}

		default:
			role, _ := m["role"].(string)
			if role == "" {
				continue
			}
			if role == "developer" {
				role = "user"
			}
			appendRegular(OllamaMessage{Role: role, Content: getMapString(m, "content")})
		}
	}
	flushPendingToolCalls()
	appendPendingReasoningMessage()
	flushDeferred()
	return messages
}

// buildResponsesMessageToOllama builds an Ollama message from a Responses API
// "message" item, flattening text parts and extracting base64 image data.
func buildResponsesMessageToOllama(m map[string]interface{}, role string) OllamaMessage {
	c, hasContent := m["content"]
	if !hasContent || c == nil {
		return OllamaMessage{Role: role, Content: ""}
	}
	if contentStr, ok := c.(string); ok {
		return OllamaMessage{Role: role, Content: contentStr}
	}
	contentArray, ok := c.([]interface{})
	if !ok {
		b, _ := json.Marshal(c)
		return OllamaMessage{Role: role, Content: string(b)}
	}

	textParts := []string{}
	images := []string{}
	for _, part := range contentArray {
		if s, ok := part.(string); ok {
			textParts = append(textParts, s)
			continue
		}
		pMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		pType, _ := pMap["type"].(string)
		switch pType {
		case "text", "input_text", "output_text":
			if t, ok := pMap["text"].(string); ok {
				textParts = append(textParts, t)
			}
		case "input_image":
			if imageURL, ok := pMap["image_url"].(string); ok && imageURL != "" {
				if strings.HasPrefix(imageURL, "data:") {
					parts := strings.SplitN(imageURL, ",", 2)
					if len(parts) == 2 {
						images = append(images, parts[1])
					}
				} else {
					log.Printf("[WARN] input_image with HTTP URL not supported for Ollama provider, skipping: %s", imageURL)
				}
			}
		case "input_file":
			if fileData, ok := pMap["file_data"].(string); ok && fileData != "" {
				if strings.HasPrefix(fileData, "data:") {
					parts := strings.SplitN(fileData, ",", 2)
					if len(parts) == 2 {
						images = append(images, parts[1])
					}
				} else {
					images = append(images, fileData)
				}
			} else if fileURL, ok := pMap["file_url"].(string); ok && fileURL != "" {
				log.Printf("[WARN] input_file with file_url not supported for Ollama provider, skipping: %s", fileURL)
			}
		}
	}
	msg := OllamaMessage{Role: role, Content: strings.Join(textParts, "")}
	if len(images) > 0 {
		msg.Images = images
	}
	return msg
}

func translateResponseToolsToOllama(tools []interface{}) []OllamaTool {
	result := []OllamaTool{}
	for _, tool := range tools {
		result = append(result, convertResponsesToolToOllamaTools(tool)...)
	}
	return result
}

// convertResponsesToolToOllamaTools is the Ollama counterpart of
// convertResponsesToolToOpenAIChatTools. Ollama only accepts "function" tools,
// so custom and namespace tools are rewrapped and qualified the same way.
func convertResponsesToolToOllamaTools(tool interface{}) []OllamaTool {
	toolMap, ok := tool.(map[string]interface{})
	if !ok {
		return nil
	}
	toolType := strings.TrimSpace(getMapString(toolMap, "type"))
	switch toolType {
	case "", "function":
		if t, ok := convertResponsesFunctionToolToOllama(toolMap, ""); ok {
			return []OllamaTool{t}
		}
	case "custom":
		if t, ok := convertResponsesCustomToolToOllama(toolMap, ""); ok {
			return []OllamaTool{t}
		}
	case "namespace":
		return convertResponsesNamespaceToolToOllama(toolMap)
	default:
		return []OllamaTool{convertResponsesBuiltinToolToOllama(toolMap, toolType)}
	}
	return nil
}

func convertResponsesFunctionToolToOllama(toolMap map[string]interface{}, overrideName string) (OllamaTool, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(toolMap)
	}
	if name == "" {
		return OllamaTool{}, false
	}
	parameters := responsesToolParameters(toolMap)
	if parameters == nil {
		parameters = map[string]interface{}{"type": "object"}
	}
	return OllamaTool{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        name,
			Description: responsesToolDescription(toolMap),
			Parameters:  parameters,
		},
	}, true
}

func convertResponsesCustomToolToOllama(toolMap map[string]interface{}, overrideName string) (OllamaTool, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(toolMap)
	}
	if name == "" {
		return OllamaTool{}, false
	}
	return OllamaTool{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        name,
			Description: responsesCustomToolDescriptionWithFormat(toolMap),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{"type": "string"},
				},
				"required": []string{"input"},
			},
		},
	}, true
}

func convertResponsesNamespaceToolToOllama(toolMap map[string]interface{}) []OllamaTool {
	namespaceName := strings.TrimSpace(getMapString(toolMap, "name"))
	children, ok := toolMap["tools"].([]interface{})
	if !ok {
		return nil
	}
	var out []OllamaTool
	for _, child := range children {
		childMap, ok := child.(map[string]interface{})
		if !ok {
			continue
		}
		childName := responsesToolName(childMap)
		qualifiedName := qualifyResponsesNamespaceToolName(namespaceName, childName)
		switch strings.TrimSpace(getMapString(childMap, "type")) {
		case "", "function":
			if t, ok := convertResponsesFunctionToolToOllama(childMap, qualifiedName); ok {
				out = append(out, t)
			}
		case "custom":
			if t, ok := convertResponsesCustomToolToOllama(childMap, qualifiedName); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

func convertResponsesBuiltinToolToOllama(toolMap map[string]interface{}, toolType string) OllamaTool {
	normalizedType := strings.ToLower(strings.TrimSpace(toolType))
	name := getMapString(toolMap, "name")
	if name == "" {
		name = nativeToolFunctionName(normalizedType)
	}
	name = sanitizeToolName(name)
	description := getMapString(toolMap, "description")
	if description == "" {
		description = nativeToolDescription(normalizedType)
	}
	parameters := toolMap["parameters"]
	if parameters == nil {
		parameters = nativeToolParameters(normalizedType)
	}
	if parameters == nil {
		parameters = map[string]interface{}{"type": "object"}
	}
	return OllamaTool{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}
}

// nativeToolFunctionName maps a Codex built-in tool type to the function name
// the upstream chat-completions model should invoke. Mirrors codex-shim's
// _responses_tool_function_name alias table (with sanitization applied by the
// caller). Plain function tools and unknown types fall back to the type itself.
func nativeToolFunctionName(toolType string) string {
	switch toolType {
	case "web_search", "web_search_preview", "computer_use", "computer_use_preview",
		"apply_patch", "local_shell":
		return toolType
	case "shell":
		return "local_shell"
	default:
		if strings.HasPrefix(toolType, "mcp") {
			return toolType
		}
		return toolType
	}
}

// buildToolTypeMap extracts a mapping from tool name to tool type from the
// request's tools array (which should already be the merged set including
// additional_tools). Used to preserve original tool types (apply_patch,
// web_search, custom, namespace children, etc.) so responses emit the correct
// output type (custom_tool_call, web_search_call, function_call).
func buildToolTypeMap(tools []interface{}) map[string]string {
	result := map[string]string{}
	var collect func(tools []interface{}, namespaceName string)
	collect = func(tools []interface{}, namespaceName string) {
		for _, tool := range tools {
			toolMap, ok := tool.(map[string]interface{})
			if !ok {
				continue
			}
			toolType := strings.TrimSpace(getMapString(toolMap, "type"))
			switch toolType {
			case "", "function":
				name := responsesToolName(toolMap)
				if name == "" {
					continue
				}
				if namespaceName != "" {
					name = qualifyResponsesNamespaceToolName(namespaceName, name)
				}
				result[sanitizeToolName(name)] = "function"
			case "custom":
				name := responsesToolName(toolMap)
				if name == "" {
					continue
				}
				if namespaceName != "" {
					name = qualifyResponsesNamespaceToolName(namespaceName, name)
				}
				result[sanitizeToolName(name)] = "custom"
			case "namespace":
				ns := strings.TrimSpace(getMapString(toolMap, "name"))
				if children, ok := toolMap["tools"].([]interface{}); ok {
					collect(children, ns)
				}
			default:
				// built-in / native tool: fall back to the tool type as the name
				// (matching codex-shim's _responses_tool_function_name).
				name := responsesToolName(toolMap)
				if name == "" {
					name = toolType
				}
				result[sanitizeToolName(name)] = strings.ToLower(strings.TrimSpace(toolType))
			}
		}
	}
	collect(tools, "")
	return result
}

// buildToolNamespaceMap builds a mapping from sanitized qualified tool name to
// the namespace name, for namespace (MCP) child tools. Used by the response
// translators to split a qualified function_call name back into name + namespace
// fields on the Responses API output item.
func buildToolNamespaceMap(tools []interface{}) map[string]string {
	result := map[string]string{}
	var collect func(tools []interface{}, namespaceName string)
	collect = func(tools []interface{}, namespaceName string) {
		for _, tool := range tools {
			toolMap, ok := tool.(map[string]interface{})
			if !ok {
				continue
			}
			toolType := strings.TrimSpace(getMapString(toolMap, "type"))
			switch toolType {
			case "namespace":
				ns := strings.TrimSpace(getMapString(toolMap, "name"))
				if children, ok := toolMap["tools"].([]interface{}); ok {
					collect(children, ns)
				}
			case "", "function", "custom":
				if namespaceName == "" {
					continue
				}
				name := responsesToolName(toolMap)
				qualified := qualifyResponsesNamespaceToolName(namespaceName, name)
				if qualified != "" {
					result[sanitizeToolName(qualified)] = namespaceName
				}
			}
		}
	}
	collect(tools, "")
	return result
}

// splitToolNamespace returns (childName, namespace) for a qualified function
// call name using the namespace map built from the request. If the name is not
// a namespace child, returns (name, "").
func splitToolNamespace(qualifiedName string, nsMap map[string]string) (string, string) {
	if nsMap == nil {
		return qualifiedName, ""
	}
	if ns, ok := nsMap[sanitizeToolName(qualifiedName)]; ok && ns != "" {
		child := qualifiedName
		switch {
		case strings.HasPrefix(child, ns+"__"):
			child = strings.TrimPrefix(child, ns+"__")
		case strings.HasSuffix(ns, "__") && strings.HasPrefix(child, ns):
			child = strings.TrimPrefix(child, ns)
		}
		if child == "" {
			child = qualifiedName
		}
		return child, ns
	}
	return qualifiedName, ""
}

// resolveToolOutputType returns the correct Responses API output item type for a
// tool call. Codex Desktop declares built-in tools like {"type":"apply_patch"}
// (freeform) and {"type":"web_search_preview"}; when the model invokes one, the
// response output item MUST come back with the matching native type or Codex
// cannot bind the call to its built-in handler and aborts the tool call.
//
// Mapping (mirrors codex-shim server.py ResponsesStreamState._open_tool):
//   - apply_patch / custom   -> custom_tool_call  (freeform; no enum validation)
//   - web_search / web_search_preview / web_search* -> web_search_call
//   - everything else        -> function_call     (incl. local_shell/shell,
//     computer_use, and plain function tools, which Codex accepts as
//     function_call items named after the tool)
func resolveToolOutputType(name string, toolTypes map[string]string) string {
	originalType := ""
	if t, ok := toolTypes[sanitizeToolName(name)]; ok {
		originalType = t
	}
	switch {
	case originalType == "apply_patch" || originalType == "custom":
		return "custom_tool_call"
	case strings.HasPrefix(originalType, "web_search"):
		return "web_search_call"
	default:
		return "function_call"
	}
}

// sanitizeToolName normalizes a tool name into the form used as a key in
// buildToolTypeMap and lookup in resolveToolOutputType (alphanumeric, _ and -
// only, max 64 chars, trimmed of surrounding underscores). Mirrors codex-shim's
// _sanitize_tool_name so request-side and response-side keys agree.
func sanitizeToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	s := b.String()
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Trim(s, "_")
}

// nativeToolParameters returns a minimal JSON-schema for a Codex built-in tool
// type when the request did not carry one (freeform/native tools have no
// caller-supplied schema). Mirrors codex-shim's _native_tool_parameters so the
// upstream chat-completions model knows which argument keys to emit.
func nativeToolParameters(toolType string) map[string]interface{} {
	switch {
	case strings.HasPrefix(toolType, "web_search") || strings.HasPrefix(toolType, "x_search"):
		return map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Search query"},
			},
			"required":             []string{"query"},
			"additionalProperties": true,
		}
	case strings.HasPrefix(toolType, "computer_use"):
		return map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{"type": "string", "description": "Computer action to perform"},
				"x":      map[string]interface{}{"type": "number", "description": "Screen x coordinate, when relevant"},
				"y":      map[string]interface{}{"type": "number", "description": "Screen y coordinate, when relevant"},
				"text":   map[string]interface{}{"type": "string", "description": "Text to type, when relevant"},
			},
			"required":             []string{"action"},
			"additionalProperties": true,
		}
	case toolType == "apply_patch":
		return map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"patch": map[string]interface{}{"type": "string", "description": applyPatchV4ADescription},
			},
			"required":             []string{"patch"},
			"additionalProperties": true,
		}
	case toolType == "local_shell" || toolType == "shell":
		return map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string", "description": "Shell command to run"},
			},
			"required":             []string{"command"},
			"additionalProperties": true,
		}
	default:
		return map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"input": map[string]interface{}{"type": "string"},
			},
			"additionalProperties": true,
		}
	}
}

// applyPatchV4ADescription is the parameter description for the apply_patch
// tool's "patch" argument. It teaches non-Codex models the V4A diff format
// that Codex Desktop's apply_patch handler expects, since saying "unified
// diff" causes models to emit standard `--- /+++ /@@` diffs that the
// handler rejects (especially for new-file creation).
const applyPatchV4ADescription = `A V4A patch string. Wrap the patch with "*** Begin Patch" and "*** End Patch" markers. Use one of these file-operation headers on its own line before the diff lines:
- "*** Add File: <path>" — create a new file; list each line prefixed with "+".
- "*** Update File: <path>" — modify an existing file; prefix added lines with "+", deleted lines with "-", and unchanged context lines with nothing.
- "*** Delete File: <path>" — delete a file (no diff lines needed).
Example:
*** Begin Patch
*** Add File: src/new.py
+print("hello")
*** End Patch`

// nativeToolDescription returns a human-readable description for a Codex
// built-in tool when the request did not carry one. Mirrors codex-shim's
// _native_tool_description.
func nativeToolDescription(toolType string) string {
	switch {
	case strings.HasPrefix(toolType, "web_search"):
		return "Search the web using Codex's web-search tool fallback."
	case strings.HasPrefix(toolType, "x_search"):
		return "Search the web for up-to-date information."
	case strings.HasPrefix(toolType, "computer_use"):
		return "Request a Codex computer-use action."
	case toolType == "apply_patch":
		return "Apply a V4A patch to the working tree. Use *** Begin Patch / *** Add File: <path> / *** Update File: <path> / *** Delete File: <path> / *** End Patch markers. Lines starting with + are additions, - are deletions, and unprefixed lines are context."
	case toolType == "local_shell" || toolType == "shell":
		return "Run a local shell command through Codex."
	case strings.HasPrefix(toolType, "mcp"):
		return "Interact with Codex MCP resources."
	default:
		return "Codex tool fallback for Responses tool type " + toolType + "."
	}
}
