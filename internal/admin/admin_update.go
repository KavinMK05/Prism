package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"ollama-proxy/internal/desktop"
)

const updateEventKeepAlive = 15 * time.Second

// handleUpdateStatus reports the app update state (idle/available/downloading)
// so the admin UI can show a toast when a new version is out. POST triggers
// the same download-and-install flow as the tray menu item.
func handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		encodeJSON(w, desktop.GetUpdateStatus())
	case http.MethodPost:
		if !desktop.TriggerAppUpdate() {
			writeJSONError(w, "no update available to install", 409)
			return
		}
		encodeJSON(w, map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func handleUpdateEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	streamUpdateEvents(w, r, desktop.SubscribeUpdateStatus, desktop.GetUpdateStatus)
}

type updateStatusSubscriber func() (<-chan desktop.UpdateStatusInfo, func())

func streamUpdateEvents(
	w http.ResponseWriter,
	r *http.Request,
	subscribe updateStatusSubscriber,
	getStatus func() desktop.UpdateStatusInfo,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	updates, cleanup := subscribe()
	defer cleanup()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	writeStatus := func(status desktop.UpdateStatusInfo) bool {
		data, err := json.Marshal(status)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeStatus(getStatus()) {
		return
	}

	ticker := time.NewTicker(updateEventKeepAlive)
	defer ticker.Stop()
	for {
		select {
		case status, ok := <-updates:
			if !ok || !writeStatus(status) {
				return
			}
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
