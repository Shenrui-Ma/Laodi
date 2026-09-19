package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsBrokenRouteDoesNotFailHookOrRunFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "current-version.json"), []byte("invalid record"), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "laodi.exe")
	if handled, err := routeWindowsExecutable(executable, []string{"hook", "--adapter", "claude-code"}); !handled || err != nil {
		t.Fatalf("broken hook route must be silently handled without fallback: %v %v", handled, err)
	}
	if handled, err := routeWindowsExecutable(executable, []string{"status"}); !handled || err == nil {
		t.Fatalf("interactive command must report broken routing: %v %v", handled, err)
	}
}

func TestWindowsUninstalledHookStillUsesLocalCommand(t *testing.T) {
	if handled, err := routeWindowsExecutable(filepath.Join(t.TempDir(), "laodi.exe"), []string{"hook"}); handled || err != nil {
		t.Fatalf("development executable should use its local command: %v %v", handled, err)
	}
}
