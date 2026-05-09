# Data models

All API data models are defined in two files:

- `models.go` — Anthropic, OpenAI Chat Completions, and Ollama native structs
- `responses_models.go` — OpenAI Responses API structs

## Anthropic request/response

```go
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
```

Anthropic messages use content blocks — a `[]interface{}` array where each element is a `map[string]interface{}` with a `type` field. Supported block types: `text`, `thinking`, `tool_use`, `tool_result`, `image`.

## OpenAI Chat Completions

```go
type OpenAIChatRequest struct {
    Model           string              `json:"model"`
    Messages        []OpenAIChatMessage `json:"messages"`
    Stream          bool                `json:"stream"`
    Temperature     *float64            `json:"temperature,omitempty"`
    TopP            *float64            `json:"top_p,omitempty"`
    MaxTokens       int                 `json:"max_tokens,omitempty"`
    Tools           []OpenAITool        `json:"tools,omitempty"`
    ResponseFormat  interface{}         `json:"response_format,omitempty"`
    ReasoningEffort string              `json:"reasoning_effort,omitempty"`
}
```

## Ollama native

```go
type OllamaChatRequest struct {
    Model    string          `json:"model"`
    Messages []OllamaMessage `json:"messages"`
    Tools    []OllamaTool    `json:"tools,omitempty"`
    Stream   bool            `json:"stream"`
    Options  *OllamaOptions  `json:"options,omitempty"`
    Think    interface{}     `json:"think,omitempty"`
    Format   interface{}     `json:"format,omitempty"`
}
```

## Responses API

```go
type ResponsesAPIRequest struct {
    Model              string          `json:"model"`
    Input              interface{}     `json:"input"`
    Instructions       interface{}     `json:"instructions,omitempty"`
    Tools              []interface{}   `json:"tools,omitempty"`
    Stream             bool            `json:"stream,omitempty"`
    Temperature        *float64        `json:"temperature,omitempty"`
    TopP               *float64        `json:"top_p,omitempty"`
    MaxOutputTokens    int             `json:"max_output_tokens,omitempty"`
    PreviousResponseID string          `json:"previous_response_id,omitempty"`
    Store              *bool           `json:"store,omitempty"`
    Reasoning          interface{}     `json:"reasoning,omitempty"`
    Text               interface{}     `json:"text,omitempty"`
}
```

The Responses API response uses a flexible `Output []interface{}` array containing `ResponsesAPIOutputMessage`, `ResponsesAPIFunctionCallItem`, and `ResponsesAPIReasoningItem` structs.
