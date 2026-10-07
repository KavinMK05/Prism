package proxy

import (
	"encoding/json"
	"unicode/utf8"
)

type AnthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	Messages      []AnthropicMessage `json:"messages"`
	System        interface{}        `json:"system,omitempty"`
	Stream        bool               `json:"stream"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	TopK          *int               `json:"top_k,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Tools         []AnthropicTool    `json:"tools,omitempty"`
	Thinking      *AnthropicThinking `json:"thinking,omitempty"`
	ToolChoice    interface{}        `json:"tool_choice,omitempty"`
	Metadata      interface{}        `json:"metadata,omitempty"`
}

type AnthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type AnthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type AnthropicImageBlock struct {
	Type   string               `json:"type"`
	Source AnthropicImageSource `json:"source"`
}

type AnthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type AnthropicToolUseBlock struct {
	Type  string                 `json:"type"`
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

type AnthropicToolResultBlock struct {
	Type      string      `json:"type"`
	ToolUseID string      `json:"tool_use_id"`
	Content   interface{} `json:"content"`
}

type AnthropicThinkingBlock struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
	// Signature is the opaque provider reasoning signature carried back to the
	// client. Claude Code echoes it verbatim on the next turn, which is how a
	// non-Anthropic upstream's chain of thought (Codex reasoning.encrypted_content,
	// Gemini thoughtSignature) survives a round trip through the Anthropic
	// protocol. Empty when the upstream has no replayable reasoning state.
	Signature string `json:"signature,omitempty"`
}

type AnthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"input_schema"`
	// Type is set for Anthropic server-side tools (e.g. "web_search_20250305",
	// "web_fetch_20250924"). Empty for ordinary function tools. Prism intercepts
	// these so they work against non-Anthropic upstreams.
	Type           string                 `json:"type,omitempty"`
	MaxUses        int                    `json:"max_uses,omitempty"`
	AllowedDomains []string               `json:"allowed_domains,omitempty"`
	BlockedDomains []string               `json:"blocked_domains,omitempty"`
	UserLocation   map[string]interface{} `json:"user_location,omitempty"`
}

type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type AnthropicResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []interface{}  `json:"content"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage `json:"usage"`
}

type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type AnthropicError struct {
	Type  string               `json:"type"`
	Error AnthropicErrorDetail `json:"error"`
}

type AnthropicErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type OllamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []OllamaMessage `json:"messages"`
	Tools    []OllamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  *OllamaOptions  `json:"options,omitempty"`
	Think    interface{}     `json:"think,omitempty"`
	Format   interface{}     `json:"format,omitempty"`
}

type OllamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Thinking   string           `json:"thinking,omitempty"`
	Images     []string         `json:"images,omitempty"`
	ToolCalls  []OllamaToolCall `json:"tool_calls,omitempty"`
	ToolName   string           `json:"tool_name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type OllamaToolCall struct {
	// Type is required by Ollama for function tool calls. ID is preserved from
	// the Anthropic tool_use block on outbound (history) messages so that
	// OpenAI-compatible cloud backends (GLM/etc. via Ollama Cloud) can correlate
	// each assistant tool_call with the following tool message's tool_call_id.
	// Ollama's own /v1/messages converter keeps this id too.
	Type     string                 `json:"type,omitempty"`
	ID       string                 `json:"id,omitempty"`
	Function OllamaToolCallFunction `json:"function"`
}

type OllamaToolCallFunction struct {
	// Index is Ollama's stable identity for a tool call within a response.
	// Ollama always emits it (even for parallel calls to the same tool, which
	// get distinct indices). It must not be omitted from the dedup key or
	// same-name calls collapse into one.
	Index     *int                   `json:"index,omitempty"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type OllamaTool struct {
	Type     string         `json:"type"`
	Function OllamaToolFunc `json:"function"`
}

type OllamaToolFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters"`
}

type OllamaOptions struct {
	NumPredict  int      `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

type OllamaChatResponse struct {
	Model           string        `json:"model"`
	CreatedAt       string        `json:"created_at"`
	Message         OllamaMessage `json:"message"`
	Error           string        `json:"error,omitempty"`
	Done            bool          `json:"done"`
	DoneReason      string        `json:"done_reason,omitempty"`
	PromptEvalCount int           `json:"prompt_eval_count,omitempty"`
	// PromptEvalCachedCount is the portion of PromptEvalCount that was served
	// from the prompt cache (Ollama 0.33+ and Ollama Cloud). PromptEvalCount
	// stays the logical input total, cache hits included — the same convention
	// as OpenAI's prompt_tokens — so the OpenAI and Responses surfaces report
	// both as-is while Anthropic subtracts the hits into
	// cache_read_input_tokens. Servers that predate cache reporting omit the
	// field entirely, which reads as zero.
	PromptEvalCachedCount int `json:"prompt_eval_cached_count,omitempty"`
	EvalCount             int `json:"eval_count,omitempty"`
}

// cachedPromptTokens returns the prompt-cache hit count, clamped to the logical
// prompt total so callers can subtract it without going negative.
func (o *OllamaChatResponse) cachedPromptTokens() int {
	if o.PromptEvalCachedCount <= 0 || o.PromptEvalCount <= 0 {
		return 0
	}
	if o.PromptEvalCachedCount > o.PromptEvalCount {
		return o.PromptEvalCount
	}
	return o.PromptEvalCachedCount
}

// promptTokensDetails builds OpenAI's prompt_tokens_details block, or nil when
// there is nothing to report so the field stays omitted.
func promptTokensDetails(cachedTokens int) *OpenAIPromptTokensDetails {
	if cachedTokens <= 0 {
		return nil
	}
	return &OpenAIPromptTokensDetails{CachedTokens: cachedTokens}
}

// estimatedReasoningTokens approximates the reasoning share of an Ollama
// response's eval_count from its thinking text. Ollama's native /api/chat
// usage carries no reasoning/output split (unlike OpenAI's
// completion_tokens_details), so the ~4-chars-per-token convention is applied;
// callers clamp the result against the reported output total. Pass the whole
// thinking text, not one delta at a time: rounding up per delta would
// over-count streams that emit many small chunks.
func estimatedReasoningTokens(thinking string) int {
	return reasoningTokensFromRunes(utf8.RuneCountInString(thinking))
}

// reasoningTokensFromRunes converts an accumulated reasoning rune count to the
// ~4-runes-per-token estimate used when the upstream reports no split.
func reasoningTokensFromRunes(runes int) int {
	if runes <= 0 {
		return 0
	}
	return (runes + 3) / 4
}

// resolveReasoningTokens picks the reasoning breakdown for a completed
// response: an upstream-reported count wins, otherwise the rune-based estimate
// of the reasoning text is used. Either way the result is clamped to the
// reported output total so the split stays self-consistent.
func resolveReasoningTokens(upstreamReported, trackedRunes, outputTokens int) int {
	if upstreamReported > 0 {
		return clampReasoningTokens(upstreamReported, outputTokens)
	}
	return clampReasoningTokens(reasoningTokensFromRunes(trackedRunes), outputTokens)
}

// clampReasoningTokens keeps a reasoning-token estimate from exceeding the
// reported output total, which would make the breakdown self-contradictory.
func clampReasoningTokens(reasoning, outputTokens int) int {
	if reasoning < 0 {
		return 0
	}
	if outputTokens > 0 && reasoning > outputTokens {
		return outputTokens
	}
	return reasoning
}

// completionTokensDetails builds OpenAI's completion_tokens_details block, or
// nil when there is no reasoning split to report so the field stays omitted.
func completionTokensDetails(reasoningTokens int) *OpenAICompletionTokensDetails {
	if reasoningTokens <= 0 {
		return nil
	}
	return &OpenAICompletionTokensDetails{ReasoningTokens: reasoningTokens}
}

type SSEEvent struct {
	Event string
	Data  string
}

type OpenAIChatRequest struct {
	Model             string               `json:"model"`
	Messages          []OpenAIChatMessage  `json:"messages"`
	Stream            bool                 `json:"stream"`
	Temperature       *float64             `json:"temperature,omitempty"`
	TopP              *float64             `json:"top_p,omitempty"`
	MaxTokens         int                  `json:"max_tokens,omitempty"`
	Stop              interface{}          `json:"stop,omitempty"`
	Tools             []OpenAITool         `json:"tools,omitempty"`
	ToolChoice        interface{}          `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    interface{}          `json:"response_format,omitempty"`
	ReasoningEffort   string               `json:"reasoning_effort,omitempty"`
	StreamOptions     *OpenAIStreamOptions `json:"stream_options,omitempty"`
}

type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ReasoningSignature is one opaque reasoning signature captured from an
// assistant turn's thinking block: a Responses-API reasoning item's
// encrypted_content. It is internal-only, never serialized into a Chat
// Completions body.
//
// CallIndex records how many of the turn's tool calls preceded this thinking
// block, so the Responses translator can replay one reasoning item per block
// at the position it belongs to. Claude Code concatenates consecutive
// assistant turns into a single message, so one message routinely carries
// several blocks; the position is what keeps each reasoning item paired with
// the calls it produced. A response-direction signature has no calls to be
// positioned against and leaves it at 0.
type ReasoningSignature struct {
	Signature string
	CallIndex int
}

type OpenAIChatMessage struct {
	Role             string           `json:"role"`
	Content          interface{}      `json:"content"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolID           string           `json:"tool_call_id,omitempty"`
	Name             string           `json:"name,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	// Reasoning is emitted by some OpenAI-compatible providers, including
	// OpenRouter, instead of reasoning_content.
	Reasoning *string `json:"reasoning,omitempty"`
	// ReasoningSignatures carries an assistant turn's reasoning signatures in
	// block order. The Responses translator replays each as a reasoning input
	// item, and the Anthropic translator re-emits them as thinking-block
	// signatures so the client round-trips the model's reasoning.
	//
	// This was a single string, which silently kept only the last signature of a
	// coalesced assistant turn: every earlier tool call then reached the
	// upstream with its chain of thought missing, and re-deriving it is what
	// reads as repeating work already done.
	ReasoningSignatures []ReasoningSignature `json:"-"`
}

type OpenAIToolCall struct {
	ID       string             `json:"id,omitempty"`
	Index    *int               `json:"index,omitempty"`
	Type     string             `json:"type"`
	Function OpenAIToolCallFunc `json:"function"`
}

type OpenAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OpenAITool struct {
	Type     string        `json:"type"`
	Function OpenAIToolDef `json:"function"`
}

type OpenAIToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters"`
}

type OpenAIChatResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   OpenAIUsage    `json:"usage"`
}

type OpenAIChoice struct {
	Index        int               `json:"index"`
	Message      OpenAIChatMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type OpenAIUsage struct {
	PromptTokens        int                        `json:"prompt_tokens"`
	CompletionTokens    int                        `json:"completion_tokens"`
	TotalTokens         int                        `json:"total_tokens"`
	PromptTokensDetails *OpenAIPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	// CompletionTokensDetails carries the reasoning-token breakdown OpenAI
	// reports on the Responses surface. Ollama Cloud omits it entirely; in
	// that case callers fall back to their own tracked reasoning output.
	CompletionTokensDetails *OpenAICompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type OpenAIPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// OpenAICompletionTokensDetails mirrors the upstream completion_tokens_details
// block (reasoning_tokens, plus vendor extras we ignore via RawMessage-free
// known fields only).
type OpenAICompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type OpenAIStreamChunk struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []OpenAIStreamChoice `json:"choices"`
	Usage   *OpenAIStreamUsage   `json:"usage,omitempty"`
	Error   json.RawMessage      `json:"error,omitempty"`
}

type OpenAIStreamUsage struct {
	PromptTokens            int                            `json:"prompt_tokens"`
	CompletionTokens        int                            `json:"completion_tokens"`
	TotalTokens             int                            `json:"total_tokens"`
	PromptTokensDetails     *OpenAIPromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *OpenAICompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type OpenAIStreamChoice struct {
	Index        int               `json:"index"`
	Delta        OpenAIStreamDelta `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type OpenAIStreamDelta struct {
	Role             string  `json:"role,omitempty"`
	Content          *string `json:"content,omitempty"`
	ReasoningContent *string `json:"reasoning_content,omitempty"`
	// Reasoning is used by some OpenAI-compatible streaming providers.
	Reasoning *string          `json:"reasoning,omitempty"`
	ToolCalls []OpenAIToolCall `json:"tool_calls,omitempty"`
}

type OpenAIErrorResponse struct {
	Error OpenAIErrorDetail `json:"error"`
}

type OpenAIErrorDetail struct {
	Message string      `json:"message"`
	Type    string      `json:"type"`
	Code    interface{} `json:"code"`
	Param   string      `json:"param,omitempty"`
}
