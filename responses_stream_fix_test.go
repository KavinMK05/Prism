package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

type bufWriter struct{ buf *bytes.Buffer }

func (b *bufWriter) Header() http.Header         { return http.Header{} }
func (b *bufWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufWriter) WriteHeader(int)             {}

func TestResponsesStreamingSequenceNumberAndItemID(t *testing.T) {
	buf := &bytes.Buffer{}
	bw := &bufWriter{buf: buf}
	e := &responsesEmitter{w: bw, flusher: nil, canFlush: false}

	// Simulate function_call argument delta + done using the helpers.
	emitToolCallDeltaEvent(e, "function_call", "fc_call_1", `{"x":`, 1)
	emitToolCallDoneEvent(e, "function_call", "fc_call_1", `{"x":1}`, 1)

	out := buf.String()
	if !strings.Contains(out, `"item_id":"fc_call_1"`) {
		t.Errorf("delta/done should use item_id, got:\n%s", out)
	}
	if strings.Contains(out, `"call_id":"fc_call_1"`) {
		t.Errorf("function_call delta/done must NOT use call_id, got:\n%s", out)
	}
	// sequence_number should be present and incrementing.
	if !strings.Contains(out, `"sequence_number":1`) || !strings.Contains(out, `"sequence_number":2`) {
		t.Errorf("expected incrementing sequence_number, got:\n%s", out)
	}
}
