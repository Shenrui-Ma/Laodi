package laodi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
)

type windowsGuardFixture struct{ home, state, root, workspace string }

func newWindowsGuardFixture(t *testing.T, artifacts bool) windowsGuardFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "用户 home")
	if err := windowsCreateDirectory(home); err != nil {
		t.Fatal(err)
	}
	f := windowsGuardFixture{home: home, state: filepath.Join(home, "state"), root: windowsArchiveRoot(home)}
	f.workspace = filepath.Join(f.root, "0123456789ab")
	if err := os.MkdirAll(f.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if artifacts {
		for _, dir := range []string{"tmp", "pending", "manifests", "extra-manifests"} {
			if err := os.Mkdir(filepath.Join(f.workspace, dir), 0700); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"checkpoint.json", "state.json", "tmp/a.tar.gz", "pending/a.tar.gz.enc", "pending/a.envelope.json", "manifests/a.json", "extra-manifests/a.json"} {
			if err := os.WriteFile(filepath.Join(f.workspace, name), []byte("SYNTHETIC_ARCHIVE_CANARY"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
			t.Errorf("guard cleanup: %v", err)
		}
	})
	return f
}
func (f windowsGuardFixture) enable(t *testing.T) {
	t.Helper()
	p, err := PlanArchiveGuard(f.home)
	if err != nil {
		t.Fatal(err)
	}
	s, err := EnableArchiveGuard(p, f.state)
	if err != nil || !s.Healthy || !s.Enabled {
		t.Fatalf("enable: %+v %v", s, err)
	}
}
func windowsGuardDenied(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected native access denied, got %v", err)
	}
}

func TestWindowsArchiveGuardNativeABA(t *testing.T) {
	f := newWindowsGuardFixture(t, true)
	before, _, err := windowsArchiveLayout(f.home)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(f.workspace, "pending", "a.tar.gz.enc")
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1024))
		if err != nil || string(b) != "SYNTHETIC_ARCHIVE_CANARY" {
			http.Error(w, "invalid fixture", 400)
			return
		}
		received.Add(1)
		w.WriteHeader(204)
	}))
	defer server.Close()
	send := func() error {
		b, err := os.ReadFile(pending)
		if err != nil {
			return err
		}
		r, err := http.Post(server.URL, "application/octet-stream", bytes.NewReader(b))
		if err != nil {
			return err
		}
		r.Body.Close()
		if r.StatusCode != 204 {
			return errors.New("receiver rejected fixture")
		}
		return nil
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	f.enable(t)
	for i := 0; i < 3; i++ {
		windowsGuardDenied(t, send())
	}
	if received.Load() != 1 {
		t.Fatal("protected payload reached receiver")
	}
	for _, path := range []string{"tmp/new", "pending/new", "tmp/a.tar.gz", "pending/a.tar.gz.enc"} {
		windowsGuardDenied(t, os.WriteFile(filepath.Join(f.workspace, path), []byte("new"), 0600))
	}
	windowsGuardDenied(t, os.Mkdir(filepath.Join(f.workspace, "new-directory"), 0700))
	if _, err := os.Stat(pending); err != nil {
		t.Fatal("pending metadata unavailable", err)
	}
	for _, path := range []string{"checkpoint.json", "state.json", "manifests/a.json", "extra-manifests/a.json"} {
		name := filepath.Join(f.workspace, path)
		if err := os.WriteFile(name, []byte("metadata continues"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadFile(name); err != nil {
			t.Fatal(err)
		}
	}
	child := filepath.Join(f.root, "abcdef012345")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	windowsGuardDenied(t, os.Mkdir(filepath.Join(child, "tmp"), 0700))
	if err := os.WriteFile(filepath.Join(child, "checkpoint.json"), []byte("new checkpoint"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := InspectArchiveGuard(f.home, f.state); err != nil || !s.Healthy {
		t.Fatalf("inspect %+v %v", s, err)
	}
	if s, err := testWindowsArchiveProtection(f.home, f.state, func() error { return nil }); err != nil || s.Status != "passed" {
		t.Fatalf("probe %+v %v", s, err)
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal(err)
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	if received.Load() != 2 {
		t.Fatal("restored upload missing")
	}
	if err := os.Mkdir(filepath.Join(child, "tmp"), 0700); err != nil {
		t.Fatal("inherited denial was not removed", err)
	}
	for _, n := range before {
		after, err := windowsArchiveMetadata(filepath.Join(f.root, n.Path), n.Path, n.Kind)
		if err != nil || !windowsArchiveSameACL(n.Before, after.Before) || n.Protected != after.Protected || n.Control != after.Control || n.Identity != after.Identity {
			t.Fatalf("original ACL/identity not restored: %s %v", n.Path, err)
		}
	}
}

func TestWindowsArchiveGuardRecoveryAndExternalEdits(t *testing.T) {
	f := newWindowsGuardFixture(t, true)
	f.enable(t)
	dir := filepath.Join(f.state, "archive-guard")
	r, err := windowsArchiveLoad(dir, f.home)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = "enabling"
	if err := windowsArchiveSave(dir, r); err != nil {
		t.Fatal(err)
	}
	if s, err := InspectArchiveGuard(f.home, f.state); err == nil || !s.RecoveryNeeded {
		t.Fatal("interruption reported healthy")
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal("interrupted change cannot recover", err)
	}
	f.enable(t)
	r, err = windowsArchiveLoad(dir, f.home)
	if err != nil {
		t.Fatal(err)
	}
	n := r.Changes[1]
	expected, err := windowsArchiveExpected(n, r.Principal)
	if err != nil {
		t.Fatal(err)
	}
	aces, _ := windowsArchiveACEs(expected)
	extra := append([]byte(nil), aces[0]...)
	extra[0] = 0 // unrelated allow ACE
	altered := windowsArchiveACL(append(aces, extra))
	path := filepath.Join(f.root, n.Path)
	windowsTestSetArchiveACL(t, path, altered)
	if s, err := DisableArchiveGuard(f.home, f.state); err == nil || !s.RecoveryNeeded {
		t.Fatal("external ACL overwritten")
	}
	current, err := windowsArchiveMetadata(path, n.Path, n.Kind)
	if err != nil || !windowsArchiveSameACL(current.Before, altered) {
		t.Fatal("external ACL was changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "receipt.json")); err != nil {
		t.Fatal("recovery receipt lost")
	}
	windowsTestSetArchiveACL(t, path, expected)
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal("recovery after external edit repair", err)
	}
}

func windowsTestSetArchiveACL(t *testing.T, path string, acl []byte) {
	t.Helper()
	h, err := windowsArchiveOpen(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(h)
	n, err := windowsArchiveRead(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := windowsArchiveSetDACL(h, acl, n.Control); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsArchiveGuardRejectsLinksAndReplacements(t *testing.T) {
	t.Run("hardlink", func(t *testing.T) {
		f := newWindowsGuardFixture(t, true)
		if err := os.Link(filepath.Join(f.workspace, "tmp", "a.tar.gz"), filepath.Join(f.home, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, err := PlanArchiveGuard(f.home); err == nil {
			t.Fatal("hardlink accepted")
		}
	})
	t.Run("renamed-pending-file", func(t *testing.T) {
		f := newWindowsGuardFixture(t, true)
		f.enable(t)
		path := filepath.Join(f.workspace, "pending", "a.tar.gz.enc")
		old := filepath.Join(f.home, "old.tar.gz.enc")
		if err := os.Rename(path, old); err != nil {
			t.Fatal(err)
		}
		// Restore the original name before the fixture cleanup retries recovery.
		defer func() {
			if err := os.Rename(old, path); err != nil {
				t.Error(err)
			}
		}()
		if s, err := DisableArchiveGuard(f.home, f.state); err == nil || !s.RecoveryNeeded || s.Healthy {
			t.Fatal("missing protected file discarded recovery", s, err)
		}
		if _, err := os.Stat(filepath.Join(f.state, "archive-guard", "receipt.json")); err != nil {
			t.Fatal("original ACL recovery record lost", err)
		}
		if _, err := os.ReadFile(old); !errors.Is(err, os.ErrPermission) {
			t.Fatal("renamed pending file did not retain its denied ACL", err)
		}
	})
}

func TestWindowsArchiveRenamedObjectsRetainRecoveryUntilRestored(t *testing.T) {
	for _, target := range []string{"pending-file", "pending-directory", "workspace", "root"} {
		t.Run(target, func(t *testing.T) {
			f := newWindowsGuardFixture(t, true)
			before, _, err := windowsArchiveLayout(f.home)
			if err != nil {
				t.Fatal(err)
			}
			f.enable(t)
			path := map[string]string{
				"pending-file":      filepath.Join(f.workspace, "pending", "a.tar.gz.enc"),
				"pending-directory": filepath.Join(f.workspace, "pending"),
				"workspace":         f.workspace,
				"root":              f.root,
			}[target]
			moved := filepath.Join(f.home, "renamed-protected-object")
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := os.Stat(moved); err == nil {
					if err := os.Rename(moved, path); err != nil {
						t.Error(err)
					}
				}
			}()
			for attempt := 0; attempt < 2; attempt++ {
				s, err := DisableArchiveGuard(f.home, f.state)
				if err == nil || !s.RecoveryNeeded || s.Healthy {
					t.Fatal("missing identity was treated as restored", s, err)
				}
				if _, err := windowsArchiveLoad(filepath.Join(f.state, "archive-guard"), f.home); err != nil {
					t.Fatal("recovery record lost or invalid", err)
				}
			}
			if err := os.Rename(moved, path); err != nil {
				t.Fatal(err)
			}
			if s, err := DisableArchiveGuard(f.home, f.state); err != nil || s.RecoveryNeeded || !s.Healthy {
				t.Fatal("restored identity could not recover", s, err)
			}
			for _, n := range before {
				current, err := windowsArchiveMetadata(filepath.Join(f.root, n.Path), n.Path, n.Kind)
				if err != nil || current.Identity != n.Identity || current.Control != n.Control || !windowsArchiveSameACL(current.Before, n.Before) {
					t.Fatal("original identity/ACL not restored", n.Path, err)
				}
			}
			if _, err := os.ReadFile(filepath.Join(f.workspace, "pending", "a.tar.gz.enc")); err != nil {
				t.Fatal("pending content remains inaccessible after recovery", err)
			}
		})
	}
}

func TestWindowsArchiveGuardPreservesGitAndBuild(t *testing.T) {
	f := newWindowsGuardFixture(t, false)
	f.enable(t)
	git := os.Getenv("LAODI_TEST_GIT")
	if git == "" {
		var err error
		git, err = exec.LookPath("git.exe")
		if err != nil {
			t.Skip("Git not available")
		}
	}
	repo := filepath.Join(f.home, "synthetic-project")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(f.home, "no-git-config"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("synthetic git: %v %s", err, out)
		}
	}
	run("init", "--quiet")
	run("config", "user.name", "Synthetic")
	run("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "fixture.txt"), []byte("synthetic history"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "synthetic baseline")
	run("status", "--porcelain")
	run("log", "-1", "--format=%s")
	run("archive", "--output", filepath.Join(f.home, "ordinary-git.tar"), "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "build.out"), []byte("synthetic build output"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsArchiveProtectionOriginalIdentity(t *testing.T) {
	app := os.Getenv("LAODI_WINDOWS_ARCHIVE_APP")
	if app == "" {
		t.Skip("original public package is opt-in")
	}
	if build, hash, err := inspectProtectionBundle(app); err != nil || build != windowsArchiveVersion || hash != windowsArchiveASAR {
		t.Fatalf("original identity %s %s %v", build, hash, err)
	}
}

func TestWindowsArchiveMetadataHandleParity(t *testing.T) {
	f := newWindowsGuardFixture(t, false)
	a, e := windowsArchiveMetadata(f.workspace, "x", "workspace")
	if e != nil {
		t.Fatal(e)
	}
	h, e := windowsArchiveOpen(f.workspace, true)
	if e != nil {
		t.Fatal(e)
	}
	defer syscall.CloseHandle(h)
	b, e := windowsArchiveRead(h)
	t.Logf("read %v mutation %v err %v", a.Protected, b.Protected, e)
	if a.Protected != b.Protected {
		t.Fatal("handle parity")
	}
}

func TestWindowsArchiveInterruptedApplyRestoresExactACLs(t *testing.T) {
	for _, count := range []int{0, 1, 3} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			f := newWindowsGuardFixture(t, true)
			nodes, _, err := windowsArchiveLayout(f.home)
			if err != nil {
				t.Fatal(err)
			}
			dir, err := windowsArchiveState(f.home, f.state, true)
			if err != nil {
				t.Fatal(err)
			}
			sid, _ := windowsCurrentUserSID()
			r := &windowsArchiveReceipt{Version: 2, Home: f.home, Principal: sid, Phase: "enabling", Changes: nodes}
			if err := windowsArchiveSave(dir, r); err != nil {
				t.Fatal(err)
			}
			for _, n := range nodes[:count] {
				if _, err := windowsArchiveApply(f.root, n, sid, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
				t.Fatal(err)
			}
			for _, n := range nodes {
				current, err := windowsArchiveMetadata(filepath.Join(f.root, n.Path), n.Path, n.Kind)
				if err != nil || !windowsArchiveSameACL(n.Before, current.Before) || n.Control != current.Control {
					t.Fatal("partial rollback changed original ACL", err)
				}
			}
		})
	}
}

func TestWindowsArchiveReplacementRetainsRecovery(t *testing.T) {
	f := newWindowsGuardFixture(t, false)
	f.enable(t)
	moved := filepath.Join(f.home, "owned-original-workspace")
	if err := os.Rename(f.workspace, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if s, err := InspectArchiveGuard(f.home, f.state); err == nil || s.Healthy {
		t.Fatal("replacement accepted")
	}
	if s, err := DisableArchiveGuard(f.home, f.state); err == nil || !s.RecoveryNeeded {
		t.Fatal("replacement ACL was adopted")
	}
	if err := os.Remove(f.workspace); err != nil {
		t.Fatal(err)
	} // empty test-owned replacement only
	if err := os.Rename(moved, f.workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsArchiveMonitorDiscoversLaterOptIn(t *testing.T) {
	f := newWindowsGuardFixture(t, false)
	monitor := newProtectionMonitor(f.home, f.state, "", nil)
	if monitor == nil {
		t.Fatal("hook-only watcher cannot discover later opt-in")
	}
	if got := monitor.check(f.home, f.state, "").Status; got != "disabled" {
		t.Fatal(got)
	}
	f.enable(t)
	// No verified client was bound by this low-level fixture. It must not claim
	// client coverage, but the running watcher must now see the new receipt.
	if got := monitor.check(f.home, f.state, "").Status; got != "unsupported_client" {
		t.Fatal(got)
	}
	if release, err := lockArchiveGuardForRemoval(f.state); err == nil {
		release()
		t.Fatal("removal allowed with active guard")
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal(err)
	}
	if got := monitor.check(f.home, f.state, "").Status; got != "disabled" {
		t.Fatal(got)
	}
}
