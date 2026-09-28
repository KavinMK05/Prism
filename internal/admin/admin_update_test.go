package admin

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ollama-proxy/internal/desktop"
)

func TestUpdateEventsInitialSnapshotAndLaterStatus(t *testing.T) {
	updates := make(chan desktop.UpdateStatusInfo, 1)
	cleaned := make(chan struct{})
	var subscribed atomic.Bool
	subscribe := func() (<-chan desktop.UpdateStatusInfo, func()) {
		subscribed.Store(true)
		return updates, func() { close(cleaned) }
	}
	initial := desktop.UpdateStatusInfo{State: "idle", Current: "v1.0.0"}
	getStatus := func() desktop.UpdateStatusInfo {
		return initial
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamUpdateEvents(w, r, subscribe, getStatus)
	}))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("open update event stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	reader := bufio.NewReader(resp.Body)
	if got := readUpdateEvent(t, reader); got.State != initial.State || got.Current != initial.Current {
		t.Fatalf("initial event = %+v, want %+v", got, initial)
	}
	if !subscribed.Load() {
		t.Fatal("initial snapshot was sent before subscription")
	}

	later := desktop.UpdateStatusInfo{State: "available", Version: "v2.0.0", Current: initial.Current}
	updates <- later
	if got := readUpdateEvent(t, reader); got != later {
		t.Fatalf("later event = %+v, want %+v", got, later)
	}

	resp.Body.Close()
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("subscription was not cleaned up after disconnect")
	}
}

func TestUpdateEventsRejectsNonGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/update/events", nil)
	resp := httptest.NewRecorder()

	handleUpdateEvents(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusMethodNotAllowed)
	}
}

func readUpdateEvent(t *testing.T, reader *bufio.Reader) desktop.UpdateStatusInfo {
	t.Helper()
	dataLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE data: %v", err)
	}
	if !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("SSE line = %q, want data prefix", dataLine)
	}
	separator, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE separator: %v", err)
	}
	if separator != "\n" {
		t.Fatalf("SSE separator = %q, want newline", separator)
	}

	var status desktop.UpdateStatusInfo
	data := strings.TrimSpace(strings.TrimPrefix(dataLine, "data: "))
	if err := json.Unmarshal([]byte(data), &status); err != nil {
		t.Fatalf("decode SSE data: %v", err)
	}
	return status
}
