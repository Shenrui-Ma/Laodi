//go:build windows

package laodi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStartupRecoveryBudgetAndStop(t *testing.T) {
	crash := errors.New("synthetic child crash")
	t.Run("bounded_crashes", func(t *testing.T) {
		count := 0
		start := time.Now()
		err := runStartupRecovery(context.Background(), func() (bool, error) { return true, nil }, func() error { count++; return crash }, 10*time.Millisecond, 3)
		if !errors.Is(err, crash) || count != 4 || time.Since(start) < 30*time.Millisecond {
			t.Fatalf("retry budget/backoff: %d %v", count, err)
		}
	})
	t.Run("normal_exit", func(t *testing.T) {
		count := 0
		err := runStartupRecovery(context.Background(), func() (bool, error) { return true, nil }, func() error { count++; return nil }, time.Minute, 3)
		if err != nil || count != 1 {
			t.Fatal("normal exit restarted")
		}
	})
	t.Run("stop_during_backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		count := 0
		err := runStartupRecovery(ctx, func() (bool, error) { return true, nil }, func() error { count++; time.AfterFunc(20*time.Millisecond, cancel); return crash }, time.Minute, 3)
		if err != nil || count != 1 {
			t.Fatal("explicit stop restarted")
		}
	})
	t.Run("authorization_revoked_between_attempts", func(t *testing.T) {
		count := 0
		err := runStartupRecovery(context.Background(), func() (bool, error) { return count == 0, nil }, func() error { count++; return crash }, time.Millisecond, 3)
		if err != nil || count != 1 {
			t.Fatal("changed startup state restarted")
		}
	})
	t.Run("uncertain_ownership", func(t *testing.T) {
		count := 0
		err := runStartupRecovery(context.Background(), func() (bool, error) { return false, crash }, func() error { count++; return nil }, time.Millisecond, 3)
		if !errors.Is(err, crash) || count != 0 {
			t.Fatal("unverified ownership launched worker")
		}
	})
}
