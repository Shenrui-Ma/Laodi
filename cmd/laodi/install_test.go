package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Shenrui-Ma/Laodi-skills/internal/laodi"
)

func TestReleaseInstallCLIHasReadOnlyPreview(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS release payload; native Windows payload has separate tests")
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	bundle := filepath.Join(t.TempDir(), "release with spaces")
	for name, mode := range map[string]os.FileMode{
		"laodi": 0700, "LaodiNotify.app/Contents/Info.plist": 0600,
		"LaodiNotify.app/Contents/MacOS/LaodiNotify": 0700, "skills/laodi/SKILL.md": 0600,
	} {
		path := filepath.Join(bundle, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic payload; never executed"), mode); err != nil {
			t.Fatal(err)
		}
	}
	output, err := os.CreateTemp(t.TempDir(), "preview")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = previous }()
	err = run([]string{"install", "--dry-run", "--source-dir", bundle, "--format", "json"})
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
	var plan laodi.DistributionPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SourceDir != bundle || plan.FileCount != 4 || !plan.RequestNotifications {
		t.Fatalf("unexpected release preview: %+v", plan)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("preview wrote into the temporary user home: %v %v", entries, err)
	}
}
