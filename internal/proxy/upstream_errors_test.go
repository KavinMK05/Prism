package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-proxy/internal/config"
)

func TestParseUpstreamResponseError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantType   string
		wantCode   interface{}
		wantInText string
	}{
		{
			name:       "openai compatible error",
			status:     401,
			body:       `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`,
			wantType:   "invalid_request_error",
			wantCode:   "invalid_api_key",
			wantInText: "Incorrect API key provided",
		},
		{
			name:       "anthropic error",
			status:     529,
			body:       `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			wantType:   "overloaded_error",
			wantInText: "Overloaded",
		},
		{
			name:       "ollama error",
			status:     404,
			body:       `{"error":"model 'missing' not found"}`,
			wantType:   "not_found_error",
			wantInText: "model 'missing' not found",
		},
		{
			name:       "generic detail",
			status:     422,
			body:       `{"detail":"The requested model is not available"}`,
			wantType:   "invalid_request_error",
			wantInText: "The requested model is not available",
		},
		{
			name:       "plain text",
			status:     503,
			body:       "service temporarily unavailable",
			wantType:   "api_error",
			wantInText: "service temporarily unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := parseUpstreamResponseError(tc.status, []byte(tc.body))
			if err.errType != tc.wantType {
				t.Fatalf("error type: got %q, want %q", err.errType, tc.wantType)
			}
			if tc.wantCode != nil && err.code != tc.wantCode {
				t.Fatalf("error code: got %#v, want %#v", err.code, tc.wantCode)
			}
			if !strings.Contains(formatUpstreamErrorMessage(err), tc.wantInText) {
				t.Fatalf("formatted message %q does not contain %q", formatUpstreamErrorMessage(err), tc.wantInText)
			}
		})
	}
}

func TestWriteOpenAIUpstreamErrorPreservesDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteOpenAIUpstreamError(rec, 429, []byte(`{"error":{"message":"Rate limit reached","type":"rate_limit_error","code":"tokens"}}`))

	if rec.Code != 429 {
		t.Fatalf("status: got %d, want 429", rec.Code)
	}
	var response OpenAIErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Type != "rate_limit_error" {
		t.Fatalf("type: got %q, want rate_limit_error", response.Error.Type)
	}
	if response.Error.Code != "tokens" {
		t.Fatalf("code: got %#v, want tokens", response.Error.Code)
	}
	if !strings.Contains(response.Error.Message, "Rate limit reached") ||
		!strings.Contains(response.Error.Message, "upstream HTTP 429") {
		t.Fatalf("message did not preserve useful details: %q", response.Error.Message)
	}
}

func TestWriteAnthropicUpstreamErrorPreservesDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteAnthropicUpstreamError(rec, 400, []byte(`{"error":{"type":"invalid_request_error","message":"model is required","param":"model"}}`))

	if rec.Code != 400 {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	var response AnthropicError
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Type != "invalid_request_error" {
		t.Fatalf("type: got %q, want invalid_request_error", response.Error.Type)
	}
	for _, want := range []string{"model is required", "upstream HTTP 400", "parameter: model"} {
		if !strings.Contains(response.Error.Message, want) {
			t.Fatalf("message %q does not contain %q", response.Error.Message, want)
		}
	}
}

func TestUpstreamErrorMessageIsBounded(t *testing.T) {
	err := parseUpstreamResponseError(500, []byte(strings.Repeat("x", 5000)))
	if len(formatUpstreamErrorMessage(err)) > 2200 {
		t.Fatalf("formatted error is too large: %d bytes", len(formatUpstreamErrorMessage(err)))
	}
}

func TestOllamaStreamingErrorIsReportedToAnthropicClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"error":"model failed to generate a response"}` + "\n"))
	}))
	defer upstream.Close()

	router := makeTestRouter(upstream.URL)
	rec := httptest.NewRecorder()
	router.handleStreaming(
		rec,
		httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", strings.NewReader(`{}`)),
		&OllamaChatRequest{Model: "test", Stream: true},
		&AnthropicRequest{Model: "test", Stream: true},
		&config.ResolvedProvider{BaseURL: upstream.URL, APIKey: "test-key", ProviderType: "ollama"},
	)

	if !strings.Contains(rec.Body.String(), "model failed to generate a response") {
		t.Fatalf("stream did not expose upstream error: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "message_delta") {
		t.Fatalf("stream emitted a normal completion after upstream error: %s", rec.Body.String())
	}
}
