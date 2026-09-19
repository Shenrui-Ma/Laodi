package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectRejectsAmbiguousCommands(t *testing.T) {
	for _, args := range [][]string{
		{"protect", "launch"}, {"protect", "enable", "extra"},
		{"protect", "status", "--format", "raw"}, {"protect", "disable", "--force"},
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
