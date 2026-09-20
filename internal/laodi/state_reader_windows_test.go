package laodi

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWindowsStateReplacementWaitsForTransientReader(t *testing.T) {
	dir := privateStateDir(t)
	if err := SaveState(dir, emptyState()); err != nil {
		t.Fatal(err)
	}
	root, err := openStateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	reader, err := openExistingStateFile(root, stateFileName, os.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	next := emptyState()
	next.LastCheckedAt = time.Now().UTC()
	done := make(chan error, 1)
	go func() { done <- SaveState(dir, next) }()
	// Windows returns ACCESS_DENIED for replacement while this verified,
	// share-delete reader is retained. Before the bounded retry fix, the
	// writer returned that error immediately instead of surviving this read.
	published := false
	select {
	case err := <-done:
		// A filesystem that can replace an open share-delete file needs no
		// retry. The regression is an early error, not an early safe success.
		if err != nil {
			t.Fatalf("reader aborted replacement before its retry budget: %v", err)
		}
		published = true
	case <-time.After(50 * time.Millisecond):
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !published {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("transient reader aborted publication: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("publication exceeded the bounded retry deadline")
		}
	}
	saved, err := LoadState(dir)
	if err != nil || !saved.LastCheckedAt.Equal(next.LastCheckedAt) {
		t.Fatalf("replacement was not persisted after the reader closed: %v", err)
	}
}

func TestWindowsStateReplacementKeepsBoundWhenReaderDoesNotClose(t *testing.T) {
	dir := privateStateDir(t)
	if err := SaveState(dir, emptyState()); err != nil {
		t.Fatal(err)
	}
	root, err := openStateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// An explicit deny-delete reader guarantees a persistent sharing conflict
	// even on filesystems that can replace a share-delete destination directly.
	reader, err := windowsOpen(filepath.Join(dir, stateFileName), os.O_RDONLY, false, false, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	next := emptyState()
	next.LastCheckedAt = time.Now().UTC()
	started := time.Now()
	err = SaveState(dir, next)
	elapsed := time.Since(started)
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, syscall.Errno(32)) && !errors.Is(err, syscall.Errno(33)) {
		t.Fatalf("persistent sharing conflict must remain an explicit failure: %v", err)
	}
	if elapsed < 250*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("replacement retry was not bounded to its existing budget: %v", elapsed)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadState(dir)
	if err != nil || !saved.LastCheckedAt.IsZero() {
		t.Fatalf("failed publication damaged previous state: %+v %v", saved, err)
	}
}
