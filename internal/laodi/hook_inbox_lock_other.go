//go:build !windows

package laodi

import (
	"os"
	"time"
)

func lockHookInbox(root *os.Root) (func(), error) {
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		release, err := AcquireLock(root.Name())
		if err == nil {
			return release, nil
		}
		if !time.Now().Before(deadline) {
			return nil, errHookInboxUnavailable
		}
		time.Sleep(5 * time.Millisecond)
	}
}
