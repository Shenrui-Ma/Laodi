package laodi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsStateUsesKnownLocalFolder(t *testing.T) {
	before, err := DefaultStateDir("")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", `\\server\share\wrong`)
	t.Setenv("APPDATA", `C:\Roaming\wrong`)
	after, err := DefaultStateDir(`C:\untrusted\home`)
	if err != nil || after != before || !localAbsoluteWindowsPath(after) || filepath.Base(after) != "Laodi-skills" {
		t.Fatalf("known folder changed: %q %v", after, err)
	}
	if DefaultClientApp("") != "" || DefaultEvidenceRoot("") != "" {
		t.Fatal("unverified client root guessed")
	}
}

func TestWindowsPathRejectsRemoteDeviceADSAndRelative(t *testing.T) {
	for _, name := range []string{`relative`, `C:relative`, `\\server\share\data`, `\\?\C:\data`, `\\.\C:\data`, `C:\data:stream`, `C:\a\..\b`, `C:\data.`, `C:\data `, `C:\NUL`, `C:\CON.txt`, "C:\\bad\nname"} {
		if localAbsoluteWindowsPath(name) {
			t.Errorf("unsafe path accepted: %q", name)
		}
	}
	if !localAbsoluteWindowsPath(`C:\中文 空格\a'b&%!$()`) {
		t.Fatal("valid local path rejected")
	}
}

func TestWindowsBuildDoesNotInheritMacOSContract(t *testing.T) {
	app := filepath.Join(t.TempDir(), "client.exe")
	if err := os.WriteFile(app, []byte("public synthetic executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if build := DetectBuild(app); build != "unknown" {
		t.Fatalf("unsupported PE acquired known build: %s", build)
	}
	r := (&Scanner{Root: t.TempDir(), App: app, Build: KnownBuild}).Scan()
	if r.Coverage != "unsupported_build" {
		t.Fatal("Windows client bypassed platform identity")
	}
}

func TestWindowsHookInstallFailsBeforeReadingSettings(t *testing.T) {
	_, err := PlanHookConfig(HookConfigOptions{Adapter: "claude-code", Home: `C:\not-inspected`, Executable: `C:\not-inspected\laodi.exe`})
	if !errors.Is(err, ErrWindowsHookContractUnverified) {
		t.Fatalf("unexpected contract result: %v", err)
	}
}

func TestWindowsPublicExecutableInspection(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := inspectClientExecutable("synthetic", self)
	if !ok || len(identity.SHA256) != 64 || identity.SnapshotCoverage != "unsupported_build" || identity.HookCoverage != "contract_unverified" {
		t.Fatalf("public executable identity unavailable: %#v", identity)
	}
	data := filepath.Join(t.TempDir(), "not-client.exe")
	if err := os.WriteFile(data, []byte("not an executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := inspectClientExecutable("synthetic", data); ok {
		t.Fatal("ordinary file recognized as executable")
	}
	result := inspectClientCandidates(ClientDiscovery{Candidates: []ClientCandidate{}}, map[string]string{self: "synthetic", strings.ToUpper(self): "synthetic"})
	if len(result.Candidates) != 1 {
		t.Fatal("case aliases duplicated executable")
	}
}

func TestWindowsVersionResourceIsIdentityNotProtocolSupport(t *testing.T) {
	program := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	identity, ok := inspectClientExecutable("synthetic-public-os-fixture", program)
	if !ok || len(strings.Split(identity.FileVersion, ".")) != 4 || identity.SnapshotCoverage != "unsupported_build" || DetectBuild(program) != "unknown" {
		t.Fatalf("public PE version was missing or treated as a supported client contract: %+v", identity)
	}
}
