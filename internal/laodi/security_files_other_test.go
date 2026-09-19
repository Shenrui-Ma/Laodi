//go:build !windows

package laodi

import (
	"os"
	"testing"
)

func assertPrivateTestPath(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("wrong private permissions on %s: %v, %v", path, info, err)
	}
}

func setPrivateTestPermissions(path string, mode os.FileMode) error { return os.Chmod(path, mode) }

func createUnsafeTestLink(target, path string) error { return os.Symlink(target, path) }

func assertUnsafeTestLink(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was changed")
	}
}
