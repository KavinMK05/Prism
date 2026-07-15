package main

type ResponsesAPIRequest struct {
	Model              string        `json:"model"`
	Input              interface{}   `json:"input"`
	Instructions       interface{}   `json:"instructions,omitempty"`
	Tools              []interface{} `json:"tools,omitempty"`
	Stream             bool          `json:"stream,omitempty"`
	Temperature        *float64      `json:"temperature,omitempty"`
	TopP               *float64      `json:"top_p,omitempty"`
	MaxOutputTokens    int           `json:"max_output_tokens,omitempty"`
	MaxToolCalls       int           `json:"max_tool_calls,omitempty"`
	PreviousResponseID string        `json:"previous_response_id,omitempty"`
	Store              *bool         `json:"store,omitempty"`
	Reasoning          interface{}   `json:"reasoning,omitempty"`
	Text               interface{}   `json:"text,omitempty"`
	ToolChoice         interface{}   `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool         `json:"parallel_tool_calls,omitempty"`
	ServiceTier        string        `json:"service_tier,omitempty"`
	PromptCacheKey     string        `json:"prompt_cache_key,omitempty"`
	Truncation         interface{}   `json:"truncation,omitempty"`
	User               interface{}   `json:"user,omitempty"`
	Metadata           interface{}   `json:"metadata,omitempty"`
	TopLogprobs        int           `json:"top_logprobs,omitempty"`
	Include            interface{}   `json:"include,omitempty"`
}

type ResponsesAPIInputMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type ResponsesAPIFunctionCallInput struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ResponsesAPIFunctionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type ResponsesAPIReasoningInput struct {
	ID      string                         `json:"id"`
	Type    string                         `json:"type"`
	Summary []ResponsesAPIReasoningSummary `json:"summary,omitempty"`
}

type ResponsesAPIReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ResponsesAPIResponse struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	CreatedAt         int64             `json:"created_at"`
	Model             string            `json:"model"`
	Status            string            `json:"status"`
	Background        bool              `json:"background"`
	Error             interface{}       `json:"error"`
	IncompleteDetails interface{}       `json:"incomplete_details"`
	Output            []interface{}     `json:"output"`
	OutputText        string            `json:"output_text,omitempty"`
	Usage             ResponsesAPIUsage `json:"usage"`
	// Request fields echoed back on the response object, per the Responses API spec.
	Instructions       interface{}   `json:"instructions,omitempty"`
	MaxOutputTokens    int           `json:"max_output_tokens,omitempty"`
	Temperature        *float64      `json:"temperature,omitempty"`
	TopP               *float64      `json:"top_p,omitempty"`
	Reasoning          interface{}   `json:"reasoning,omitempty"`
	Text               interface{}   `json:"text,omitempty"`
	ToolChoice         interface{}   `json:"tool_choice,omitempty"`
	Tools              []interface{} `json:"tools,omitempty"`
	ParallelToolCalls  *bool         `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID string        `json:"previous_response_id,omitempty"`
	Store              *bool         `json:"store,omitempty"`
	ServiceTier        string        `json:"service_tier,omitempty"`
	PromptCacheKey     string        `json:"prompt_cache_key,omitempty"`
	Truncation         interface{}   `json:"truncation,omitempty"`
	User               interface{}   `json:"user,omitempty"`
	Metadata           interface{}   `json:"metadata,omitempty"`
	Meta               interface{}   `json:"meta,omitempty"`
}

type ResponsesAPIUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type ResponsesAPIOutputMessage struct {
	ID      string                    `json:"id"`
	Type    string                    `json:"type"`
	Status  string                    `json:"status"`
	Role    string                    `json:"role"`
	Content []ResponsesAPIContentPart `json:"content"`
}

type ResponsesAPIContentPart struct {
	Type        string        `json:"type"`
	Text        string        `json:"text,omitempty"`
	Annotations []interface{} `json:"annotations,omitempty"`
}

type ResponsesAPIFunctionCallItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Status    string `json:"status,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type ResponsesAPIReasoningItem struct {
	ID      string                         `json:"id"`
	Type    string                         `json:"type"`
	Summary []ResponsesAPIReasoningSummary `json:"summary,omitempty"`
}

type ResponsesAPITextFormat struct {
	Type       string      `json:"type"`
	JSONSchema interface{} `json:"json_schema,omitempty"`
}

type ResponsesAPIReasoningConfig struct {
	Effort string `json:"effort,omitempty"`
}
