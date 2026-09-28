package desktop

import (
	"testing"
	"time"
)

func TestSubscribeUpdateStatusDeliveryAndCancellation(t *testing.T) {
	updateMu.Lock()
	originalState := updateState
	originalInfo := updateInfo
	updateState = UpdateAvailable
	updateInfo = &UpdateInfo{Version: "v2.0.0"}
	updateMu.Unlock()
	t.Cleanup(func() {
		updateMu.Lock()
		updateState = originalState
		updateInfo = originalInfo
		updateMu.Unlock()
	})

	updates, cleanup := SubscribeUpdateStatus()
	defer cleanup()

	notifyUpdateStatus()
	select {
	case got := <-updates:
		if got.State != "available" || got.Version != "v2.0.0" || got.Current != version {
			t.Fatalf("unexpected update status: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for update status")
	}

	cleanup()
	cleanup()
	if _, ok := <-updates; ok {
		t.Fatal("update subscription channel remained open after cleanup")
	}

	notifyUpdateStatus()
}
