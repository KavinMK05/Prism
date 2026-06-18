package main

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
	Type   string                `json:"type"`
	Source AnthropicImageSource  `json:"source"`
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
}

type AnthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"input_schema"`
}

type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type AnthropicResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []interface{}    `json:"content"`
	StopReason   string           `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage   `json:"usage"`
}

type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type AnthropicError struct {
	Type  string              `json:"type"`
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
	Role       string                 `json:"role"`
	Content    string                 `json:"content"`
	Thinking   string                 `json:"thinking,omitempty"`
	Images     []string               `json:"images,omitempty"`
	ToolCalls  []OllamaToolCall       `json:"tool_calls,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
}

type OllamaToolCall struct {
	ID       string                 `json:"id,omitempty"`
	Function OllamaToolCallFunction `json:"function"`
}

type OllamaToolCallFunction struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type OllamaTool struct {
	Type     string         `json:"type"`
	Function OllamaToolFunc `json:"function"`
}

type OllamaToolFunc struct {
	Name        string      `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters"`
}

type OllamaOptions struct {
	NumPredict  int       `json:"num_predict,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	TopK        *int      `json:"top_k,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
}

type OllamaChatResponse struct {
	Model          string        `json:"model"`
	CreatedAt       string        `json:"created_at"`
	Message         OllamaMessage `json:"message"`
	Done           bool          `json:"done"`
	DoneReason     string        `json:"done_reason,omitempty"`
	PromptEvalCount int          `json:"prompt_eval_count,omitempty"`
	EvalCount      int          `json:"eval_count,omitempty"`
}

type SSEEvent struct {
	Event string
	Data  string
}

type OpenAIChatRequest struct {
	Model            string              `json:"model"`
	Messages         []OpenAIChatMessage `json:"messages"`
	Stream           bool                `json:"stream"`
	Temperature      *float64            `json:"temperature,omitempty"`
	TopP             *float64            `json:"top_p,omitempty"`
	MaxTokens        int                 `json:"max_tokens,omitempty"`
	Tools            []OpenAITool        `json:"tools,omitempty"`
	ResponseFormat   interface{}         `json:"response_format,omitempty"`
	ReasoningEffort  string             `json:"reasoning_effort,omitempty"`
	StreamOptions    *OpenAIStreamOptions `json:"stream_options,omitempty"`
}

type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type OpenAIChatMessage struct {
	Role             string                 `json:"role"`
	Content          interface{}            `json:"content"`
	ToolCalls        []OpenAIToolCall       `json:"tool_calls,omitempty"`
	ToolID           string                 `json:"tool_call_id,omitempty"`
	Name             string                 `json:"name,omitempty"`
	ReasoningContent *string                `json:"reasoning_content,omitempty"`
}

type OpenAIToolCall struct {
	ID       string                `json:"id,omitempty"`
	Index    *int                  `json:"index,omitempty"`
	Type     string                `json:"type"`
	Function OpenAIToolCallFunc    `json:"function"`
}

type OpenAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OpenAITool struct {
	Type     string      `json:"type"`
	Function OpenAIToolDef `json:"function"`
}

type OpenAIToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters"`
}

type OpenAIChatResponse struct {
	ID      string             `json:"id"`
	Object string             `json:"object"`
	Model   string             `json:"model"`
	Choices []OpenAIChoice    `json:"choices"`
	Usage   OpenAIUsage        `json:"usage"`
}

type OpenAIChoice struct {
	Index        int              `json:"index"`
	Message      OpenAIChatMessage `json:"message"`
	FinishReason string           `json:"finish_reason"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAIStreamChunk struct {
	ID       string               `json:"id"`
	Object   string               `json:"object"`
	Created  int64                `json:"created"`
	Model    string               `json:"model"`
	Choices  []OpenAIStreamChoice `json:"choices"`
	Usage    *OpenAIStreamUsage   `json:"usage,omitempty"`
}

type OpenAIStreamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAIStreamChoice struct {
	Index        int                    `json:"index"`
	Delta        OpenAIStreamDelta      `json:"delta"`
	FinishReason *string                `json:"finish_reason"`
}

type OpenAIStreamDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
}

type OpenAIErrorResponse struct {
	Error OpenAIErrorDetail `json:"error"`
}

type OpenAIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    int    `json:"code"`
}