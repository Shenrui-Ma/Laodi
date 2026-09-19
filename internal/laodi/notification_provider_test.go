package laodi

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchCanEnableNotificationWithoutRestartingOrReplayingHistory(t *testing.T) {
	data := filepath.Join(t.TempDir(), "state")
	calls := filepath.Join(t.TempDir(), "calls")
	helper := syntheticNotifier(t, calls, "calls")
	var enabled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, &Scanner{}, WatchOptions{StateDir: data, Interval: time.Second, HooksOnly: true, NotifierForEvent: func() string {
			if enabled.Load() {
				return helper
			}
			return ""
		}})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitFor := func(count int, status string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			state, err := LoadState(data)
			if err == nil && len(state.Events) == count && state.Events[count-1].Notification == status {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("notification transition not persisted: %+v %v", state, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := SubmitHookInspection(data, runtimeHook(t, "zcode", "PostToolUse", "before-enable")); err != nil {
		t.Fatal(err)
	}
	waitFor(1, "not_configured")
	enabled.Store(true)
	if err := SubmitHookInspection(data, runtimeHook(t, "zcode", "PostToolUse", "after-enable")); err != nil {
		t.Fatal(err)
	}
	waitFor(2, "accepted_by_os")
	state, err := LoadState(data)
	if err != nil || state.Events[0].Notification != "not_configured" {
		t.Fatal("enabling replayed older notifications", err)
	}
	b, err := os.ReadFile(calls)
	if err != nil || string(b) != "called\n" {
		t.Fatalf("notification did not stay limited to new event: %q %v", b, err)
	}
}
