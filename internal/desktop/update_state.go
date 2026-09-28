package desktop

import "sync"

// UpdateStatusInfo is the admin-API view of the app update state.
type UpdateStatusInfo struct {
	State   string `json:"state"`   // idle | checking | available | downloading | ready | failed
	Version string `json:"version"` // latest version when state is available
	Current string `json:"current"`
	Error   string `json:"error,omitempty"`
}

var updateSubscribers = make(map[chan UpdateStatusInfo]struct{})
var updateSubscribersMu sync.Mutex

// SubscribeUpdateStatus registers a buffered subscriber for app update changes.
// The returned cleanup function is safe to call more than once.
func SubscribeUpdateStatus() (<-chan UpdateStatusInfo, func()) {
	updates := make(chan UpdateStatusInfo, 8)

	updateSubscribersMu.Lock()
	updateSubscribers[updates] = struct{}{}
	updateSubscribersMu.Unlock()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			updateSubscribersMu.Lock()
			delete(updateSubscribers, updates)
			close(updates)
			updateSubscribersMu.Unlock()
		})
	}
	return updates, cleanup
}

func notifyUpdateStatus() {
	info := GetUpdateStatus()

	updateSubscribersMu.Lock()
	defer updateSubscribersMu.Unlock()
	for updates := range updateSubscribers {
		select {
		case updates <- info:
		default:
		}
	}
}

// GetUpdateStatus returns a snapshot of the current update flow state.
func GetUpdateStatus() UpdateStatusInfo {
	updateMu.Lock()
	defer updateMu.Unlock()

	info := UpdateStatusInfo{State: "idle", Current: version}
	switch updateState {
	case UpdateChecking:
		info.State = "checking"
	case UpdateAvailable:
		info.State = "available"
		if updateInfo != nil {
			info.Version = updateInfo.Version
		}
	case UpdateDownloading:
		info.State = "downloading"
		if updateInfo != nil {
			info.Version = updateInfo.Version
		}
	case UpdateReady:
		info.State = "ready"
	case UpdateFailed:
		info.State = "failed"
		info.Error = "update failed - retry from the tray icon"
	}
	return info
}

// TriggerAppUpdate kicks off the download-and-install flow (same as clicking
// the tray item). Returns false when an update is not available.
func TriggerAppUpdate() bool {
	updateMu.Lock()
	if updateState != UpdateAvailable || updateInfo == nil {
		updateMu.Unlock()
		return false
	}
	updateMu.Unlock()

	go installUpdate()
	return true
}
