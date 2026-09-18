package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Shenrui-Ma/Laodi-skills/internal/laodi"
)

// People investigating the public cases need existing evidence before installing
// a daemon. This exercises the CLI's read-only, redacted initial-check path.
func TestCheckShowsExistingAcceptanceWithoutInstallingOrWritingState(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "0123456789ab")
	hash := strings.Repeat("a", 64)
	if err := os.MkdirAll(filepath.Join(ws, "manifests"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]any{
		"manifests/" + hash + ".json": map[string]any{
			"schema": "repo_snapshot_manifest/v2", "workspaceKey": "/SYNTHETIC/private-project",
			"files": []map[string]any{{"path": "source.txt", "sizeBytes": 7}},
		},
		"state.json": map[string]any{
			"workspaceKey": "/SYNTHETIC/private-project", "workspacePath": "/SYNTHETIC/private-project",
			"lastAcceptedManifestHash": hash,
		},
	} {
		b, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(t.TempDir(), "not-created")
	output, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = previous }()
	err = run([]string{"check", "--root", root, "--state-dir", data, "--build", laodi.KnownBuild, "--format", "agent-summary"})
	os.Stdout = previous
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	var summary laodi.AgentSummary
	if err := json.Unmarshal(b, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Counts["workspace_snapshot_upload_acceptance_recorded"] != 1 || summary.Counts["upload_acceptance_recorded"] != 0 {
		t.Fatalf("existing non-Git acceptance not accurately reported: %+v", summary)
	}
	for _, value := range []string{root, "SYNTHETIC", "private-project", "source.txt", hash} {
		if strings.Contains(string(b), value) {
			t.Fatalf("private fixture data exposed: %q", value)
		}
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("read-only check wrote monitor state: %v", err)
	}
}
