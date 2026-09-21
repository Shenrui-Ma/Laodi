package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProtectRejectsAmbiguousCommands(t *testing.T) {
	for _, args := range [][]string{
		{"protect", "launch"}, {"protect", "enable", "extra"},
		{"protect", "status", "--format", "raw"}, {"protect", "disable", "--force"},
		{"protect", "test", "--dry-run"}, {"protect", "status", "--notify"},
		{"protect", "test", "--notifier", "/unused/helper"},
		{"protect", "test", "--notify", "--notifier", "relative/helper"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestProtectStatusDoesNotCreateStateOrExposePaths(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "PRIVATE-unused")
	f, err := os.CreateTemp(dir, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	previous := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = previous }()
	err = run([]string{"protect", "status", "--state-dir", state, "--format", "agent-summary"})
	os.Stdout = previous
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		if err == nil || !strings.Contains(err.Error(), "supports macOS only") || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("unsupported platform protection not reported safely: %v", err)
		}
		if info, statErr := f.Stat(); statErr != nil || info.Size() != 0 {
			t.Fatal("unsupported protection emitted a success summary")
		}
		if _, statErr := os.Stat(state); !os.IsNotExist(statErr) {
			t.Fatal("unsupported protection query wrote state")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "disabled" || strings.Contains(string(b), "PRIVATE") {
		t.Fatalf("unexpected summary %s", b)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("query wrote state")
	}
}
