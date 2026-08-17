package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

// upstreamResponseError is the provider-agnostic representation of an error
// returned by an upstream API. OpenAI-compatible providers, Anthropic, Ollama,
// and most gateways use slightly different JSON shapes, so the proxy
// normalizes them before returning an error to the client.
type upstreamResponseError struct {
	status  int
	message string
	errType string
	code    interface{}
	param   string
}

func (e *upstreamResponseError) Error() string {
	return formatUpstreamErrorMessage(e)
}

// WriteAnthropicUpstreamError returns the useful details from an upstream
// response instead of hiding them behind "Upstream returned status N".
func WriteAnthropicUpstreamError(w http.ResponseWriter, status int, body []byte) {
	err := parseUpstreamResponseError(status, body)
	WriteAnthropicError(w, status, err.errType, formatUpstreamErrorMessage(err))
}

// WriteOpenAIUpstreamError returns the upstream's message, type, and provider
// error code in the OpenAI error shape.
func WriteOpenAIUpstreamError(w http.ResponseWriter, status int, body []byte) {
	err := parseUpstreamResponseError(status, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(OpenAIErrorResponse{
		Error: OpenAIErrorDetail{
			Message: formatUpstreamErrorMessage(err),
			Type:    err.errType,
			Code:    errorCodeOrStatus(err),
			Param:   err.param,
		},
	})
}

// WriteAnthropicUpstreamFailure and WriteOpenAIUpstreamFailure preserve a
// parsed upstream error when a helper has already turned the HTTP response
// into an error value, while still handling connection failures normally.
func WriteAnthropicUpstreamFailure(w http.ResponseWriter, status int, cause error) {
	var upstreamErr *upstreamResponseError
	if errors.As(cause, &upstreamErr) {
		WriteAnthropicError(w, status, upstreamErr.errType, formatUpstreamErrorMessage(upstreamErr))
		return
	}
	WriteAnthropicError(w, status, "api_error", "Upstream request failed: "+cause.Error())
}

func WriteOpenAIUpstreamFailure(w http.ResponseWriter, status int, cause error) {
	var upstreamErr *upstreamResponseError
	if errors.As(cause, &upstreamErr) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(OpenAIErrorResponse{
			Error: OpenAIErrorDetail{
				Message: formatUpstreamErrorMessage(upstreamErr),
				Type:    upstreamErr.errType,
				Code:    errorCodeOrStatus(upstreamErr),
				Param:   upstreamErr.param,
			},
		})
		return
	}
	WriteOpenAIError(w, status, "server_error", "Upstream request failed: "+cause.Error())
}

func writeStreamingOpenAIUpstreamError(w http.ResponseWriter, status int, body []byte, model string) {
	err := parseUpstreamResponseError(status, body)
	writeStreamingOpenAIErrorWithCode(w, status, err.errType, formatUpstreamErrorMessage(err), errorCodeOrStatus(err), model)
}

func parseUpstreamResponseError(status int, body []byte) *upstreamResponseError {
	result := &upstreamResponseError{
		status:  status,
		errType: upstreamErrorTypeForStatus(status),
	}

	var payload interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&payload) == nil {
		if object, ok := payload.(map[string]interface{}); ok {
			// The useful error object may be top-level (Ollama), nested under
			// "error" (OpenAI/Anthropic), or nested under "detail".
			errorObject := object
			if nested, ok := object["error"].(map[string]interface{}); ok {
				errorObject = nested
			}
			result.message = firstString(errorObject, "message", "detail", "error_description")
			if result.message == "" {
				if value, ok := object["error"].(string); ok {
					result.message = value
				}
			}
			if parsedType := firstString(errorObject, "type", "status", "category"); parsedType != "" {
				result.errType = parsedType
			} else if parsedType := firstString(object, "type", "status"); parsedType != "" {
				result.errType = parsedType
			}
			result.code = errorValue(errorObject, "code")
			if result.code == nil {
				result.code = errorValue(object, "code")
			}
			result.param = firstString(errorObject, "param", "parameter", "field")
		}
	}

	if result.message == "" {
		result.message = cleanUpstreamText(string(body))
	}
	if result.message == "" {
		text := http.StatusText(status)
		if text == "" {
			text = "unknown error"
		}
		result.message = fmt.Sprintf("Upstream provider returned HTTP %d (%s)", status, text)
	}
	return result
}

func firstString(object map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func errorValue(object map[string]interface{}, key string) interface{} {
	value, ok := object[key]
	if !ok {
		return nil
	}
	switch value := value.(type) {
	case json.Number:
		if integer, err := strconv.ParseInt(string(value), 10, 64); err == nil {
			return integer
		}
		return string(value)
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return value
	default:
		return value
	}
}

func errorCodeOrStatus(err *upstreamResponseError) interface{} {
	if err.code != nil {
		return err.code
	}
	return err.status
}

func formatUpstreamErrorMessage(err *upstreamResponseError) string {
	message := cleanUpstreamText(err.message)
	if message == "" {
		text := http.StatusText(err.status)
		if text == "" {
			text = "unknown error"
		}
		message = fmt.Sprintf("Upstream provider returned HTTP %d (%s)", err.status, text)
	}

	details := fmt.Sprintf("upstream HTTP %d", err.status)
	if err.code != nil {
		details += ", code: " + fmt.Sprint(err.code)
	}
	if err.param != "" {
		details += ", parameter: " + err.param
	}
	return message + " (" + details + ")"
}

func cleanUpstreamText(value string) string {
	value = strings.TrimFunc(value, unicode.IsSpace)
	if value == "" {
		return ""
	}
	// Keep client errors readable and prevent a provider's huge HTML/debug
	// response from becoming the error message sent to every client.
	value = strings.Join(strings.Fields(value), " ")
	const maxErrorMessageLength = 2000
	if len(value) > maxErrorMessageLength {
		return value[:maxErrorMessageLength] + "..."
	}
	return value
}

func upstreamErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "timeout_error"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		if status >= 400 && status < 500 {
			return "invalid_request_error"
		}
		return "api_error"
	}
}
