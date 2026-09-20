//go:build windows

package laodi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func windowsSnapshotProgramFixture(t *testing.T) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "ZCode.exe")
	// Public OS executable supplies a real PE version resource. It is never
	// executed and cannot acquire the reviewed ZCode content identity.
	data, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range windowsSnapshotProgramPaths(app)[1:] {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("synthetic program bytes; never executed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

func TestWindowsSnapshotIdentityIncludesExecutableArchiveAndRuntime(t *testing.T) {
	app := windowsSnapshotProgramFixture(t)
	first := DetectBuild(app)
	if !strings.HasPrefix(first, "windows:zcode:") || first == KnownBuild || first == windowsZCodeReviewedBuild {
		t.Fatalf("synthetic public programs acquired wrong identity: %s", first)
	}
	s := &Scanner{App: app, Root: filepath.Join(t.TempDir(), "must-not-read")}
	if r := s.Scan(); r.Coverage != "unsupported_build" || r.Artifacts != 0 || s.Build != first {
		t.Fatalf("unverified public program was interpreted: %+v", r)
	}
	for _, name := range windowsSnapshotProgramPaths(app)[1:] {
		if err := os.WriteFile(name, []byte("changed public synthetic program contents"), 0600); err != nil {
			t.Fatal(err)
		}
		prior := s.Build
		if r := s.Scan(); r.Coverage != "unsupported_build" || s.Build == prior || s.Build == "unknown" {
			t.Fatalf("changed JS resource did not invalidate cached build: %+v", r)
		}
	}
	if err := os.Remove(windowsSnapshotProgramPaths(app)[2]); err != nil {
		t.Fatal(err)
	}
	if r := s.Scan(); r.Coverage != "unsupported_build" || s.Build != "unknown" {
		t.Fatalf("missing runtime left recognized build cached: %+v", r)
	}
}

func TestWindowsSnapshotBuildGateSeparatesReviewedIdentityFromProtocol(t *testing.T) {
	if !supportedSnapshotBuild(KnownBuild, "") || supportedSnapshotBuild(KnownBuild, `C:\app\ZCode.exe`) || supportedSnapshotBuild(windowsZCodeReviewedBuild, "") {
		t.Fatal("platform gate lost explicit synthetic override or trusted an incompatible protocol")
	}
	r := (&Scanner{Build: windowsZCodeReviewedBuild, Root: filepath.Join(t.TempDir(), "must-not-read")}).Scan()
	if r.Coverage != "unsupported_build" || r.Artifacts != 0 || len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != "windows_zcode_git_checkpoint_schema_unsupported" {
		t.Fatalf("reviewed Git checkpoints were mistaken for upload evidence: %+v", r)
	}
	if unsupportedSnapshotBuildDiagnostic(windowsZCodeReviewedBuild+"modified") != "zcode_build_not_verified" {
		t.Fatal("unknown content acquired the reviewed protocol diagnosis")
	}
}

func TestWindowsSnapshotProgramHashRejectsPathAliases(t *testing.T) {
	app := windowsSnapshotProgramFixture(t)
	link := filepath.Join(filepath.Dir(app), "alias.exe")
	if err := os.Link(app, link); err != nil {
		t.Fatal(err)
	}
	if _, err := hashWindowsSnapshotProgram(app); err == nil {
		t.Fatal("hardlinked client resource accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if _, err := hashWindowsSnapshotProgram(app + ":stream"); err == nil {
		t.Fatal("alternate data stream accepted")
	}
	alias := filepath.Join(t.TempDir(), "junction")
	if err := createUnsafeTestLink(filepath.Dir(app), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := hashWindowsSnapshotProgram(filepath.Join(alias, "ZCode.exe")); err == nil {
		t.Fatal("junction client resource accepted")
	}
}

// Opt-in only: this checks public installed program bytes, without executing
// the client or reading any checkpoint, settings, login or session data.
func TestWindowsInstalledSnapshotProgramIdentity(t *testing.T) {
	app := os.Getenv("LAODI_TEST_ZCODE_APP")
	if app == "" {
		t.Skip("set LAODI_TEST_ZCODE_APP to the reviewed public ZCode.exe")
	}
	if build := DetectBuild(app); build != windowsZCodeReviewedBuild {
		t.Fatalf("installed public program differs from reviewed content: %s", build)
	}
	r := (&Scanner{App: app, Root: filepath.Join(t.TempDir(), "must-not-read")}).Scan()
	if r.Coverage != "unsupported_build" || len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != "windows_zcode_git_checkpoint_schema_unsupported" {
		t.Fatalf("installed client did not report its known protocol mismatch: %+v", r)
	}
}
