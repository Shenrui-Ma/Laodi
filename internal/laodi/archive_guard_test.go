package laodi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type archiveFixture struct{ home, state, root, workspace, principal string }

func newArchiveFixture(t *testing.T, artifacts bool) archiveFixture {
	t.Helper()
	if runtime.GOOS != "darwin" || os.Getuid() <= 0 {
		t.Skip("native macOS ACL fixture requires a normal macOS user")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := archiveFixture{home: home, state: filepath.Join(home, "laodi-state"), root: filepath.Join(home, ".zcode", "v2", "checkpoints")}
	f.workspace = filepath.Join(f.root, "0123456789ab")
	f.principal, err = archivePrincipal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if artifacts {
		for _, name := range []string{"tmp", "pending", "manifests", "extra-manifests"} {
			if err := os.Mkdir(filepath.Join(f.workspace, name), 0700); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"checkpoint.json", "state.json", "tmp/work.tar.gz", "pending/work.tar.gz.enc", "pending/work.envelope.json", "manifests/old.json", "extra-manifests/old.json"} {
			if err := os.WriteFile(filepath.Join(f.workspace, name), []byte("synthetic fixture"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

func (f archiveFixture) enable(t *testing.T) ArchiveGuardResult {
	t.Helper()
	plan, err := PlanArchiveGuard(f.home)
	if err != nil {
		t.Fatal(err)
	}
	result, err := EnableArchiveGuard(plan, f.state)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Enabled || !result.Healthy || result.RecoveryNeeded {
		t.Fatalf("unexpected enabled result: %+v", result)
	}
	return result
}

func archiveTestCommand(t *testing.T, args ...string) {
	t.Helper()
	if _, err := archiveCommand(nil, "/bin/chmod", args...); err != nil {
		t.Fatal(err)
	}
}

func archiveWantDenied(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected EACCES, got %v", err)
	}
}

func archiveTestACL(t *testing.T, path string) []string {
	t.Helper()
	acl, err := archiveReadACL(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return acl
}

func TestArchiveGuardNativePreservesCheckpointFilesAndUnrelatedACLs(t *testing.T) {
	f := newArchiveFixture(t, true)
	archiveTestCommand(t, "+a#", "0", f.principal+" allow readattr", f.root)
	archiveTestCommand(t, "+a#", "0", f.principal+" allow readextattr", f.workspace)
	beforeRoot, beforeHash := archiveTestACL(t, f.root), archiveTestACL(t, f.workspace)
	result := f.enable(t)
	if result.ProtectedWorkspaces != 1 || result.ProtectedArtifacts != 5 || !result.Changed {
		t.Fatalf("wrong coverage: %+v", result)
	}
	for _, name := range []string{"tmp/new", "pending/new"} {
		archiveWantDenied(t, os.WriteFile(filepath.Join(f.workspace, name), []byte("new"), 0600))
	}
	archiveWantDenied(t, os.Mkdir(filepath.Join(f.workspace, "new-dir"), 0700))
	archiveWantDenied(t, os.Mkdir(filepath.Join(f.workspace, "tmp", "new-dir"), 0700))
	archiveWantDenied(t, os.WriteFile(filepath.Join(f.workspace, "tmp", "work.tar.gz"), []byte("overwrite"), 0600))
	appendFile, err := os.OpenFile(filepath.Join(f.workspace, "tmp", "work.tar.gz"), os.O_APPEND|os.O_WRONLY, 0)
	if appendFile != nil {
		appendFile.Close()
	}
	archiveWantDenied(t, err)
	if _, err := os.ReadFile(filepath.Join(f.workspace, "tmp", "work.tar.gz")); err != nil {
		t.Fatalf("tmp content reading was unexpectedly denied: %v", err)
	}
	for _, name := range []string{"work.tar.gz.enc", "work.envelope.json"} {
		path := filepath.Join(f.workspace, "pending", name)
		_, err := os.ReadFile(path)
		archiveWantDenied(t, err)
		archiveWantDenied(t, os.WriteFile(path, []byte("overwrite"), 0600))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("metadata access was denied: %v", err)
		}
	}
	if _, err := os.ReadDir(filepath.Join(f.workspace, "pending")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{f.workspace, filepath.Join(f.root, "abcdefabcdef")} {
		if dir != f.workspace {
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		for _, child := range []string{"tmp", "pending", "manifests", "extra-manifests"} {
			if dir != f.workspace {
				archiveWantDenied(t, os.Mkdir(filepath.Join(dir, child), 0700))
			}
		}
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		archiveWantDenied(t, os.Mkdir(filepath.Join(dir, "after-chmod"), 0700))
		tmp, final := filepath.Join(dir, "checkpoint.json.123.tmp"), filepath.Join(dir, "checkpoint.json")
		if err := os.WriteFile(tmp, []byte("normal checkpoint"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, final); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(final); err != nil || string(data) != "normal checkpoint" {
			t.Fatalf("checkpoint content mismatch: %v", err)
		}
		if err := os.Remove(final); err != nil {
			t.Fatal(err)
		}
	}
	status, err := InspectArchiveGuard(f.home, f.state)
	if err != nil || !status.Healthy || status.ProtectedWorkspaces != 2 {
		t.Fatalf("new workspace inheritance missing: %+v, %v", status, err)
	}
	if result := f.enable(t); result.Changed {
		t.Fatal("repeated enable mutated ACLs")
	}
	disabled, err := DisableArchiveGuard(f.home, f.state)
	if err != nil || disabled.Enabled || disabled.RecoveryNeeded || !disabled.Changed {
		t.Fatalf("disable result: %+v %v", disabled, err)
	}
	if !reflect.DeepEqual(archiveTestACL(t, f.root), beforeRoot) || !reflect.DeepEqual(archiveTestACL(t, f.workspace), beforeHash) {
		t.Fatal("unrelated ACLs changed")
	}
	if len(archiveTestACL(t, filepath.Join(f.root, "abcdefabcdef"))) != 0 {
		t.Fatal("owned inherited child ACL left behind")
	}
	for _, name := range []string{"tmp/work.tar.gz", "pending/work.tar.gz.enc", "pending/work.envelope.json"} {
		data, err := os.ReadFile(filepath.Join(f.workspace, name))
		if err != nil || string(data) != "synthetic fixture" {
			t.Fatalf("artifact was changed or is still blocked: %s %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(f.root, "abcdefabcdef", "tmp"), 0700); err != nil {
		t.Fatal("child ACL was not removed", err)
	}
	if _, err := os.Stat(filepath.Join(f.state, "archive-guard", "receipt.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt survived successful disable")
	}
	if result, err := DisableArchiveGuard(f.home, f.state); err != nil || result.Changed {
		t.Fatalf("disable is not idempotent: %+v %v", result, err)
	}
}

func TestArchiveGuardNativeCreatesOnlyFixedRoot(t *testing.T) {
	f := newArchiveFixture(t, false)
	if err := os.Remove(f.workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.root); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanArchiveGuard(f.home)
	if err != nil || !plan.CreatesRoot {
		t.Fatalf("missing root plan: %+v %v", plan, err)
	}
	if _, err := os.Stat(f.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("planning created root")
	}
	if _, err := EnableArchiveGuard(plan, f.state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.root)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("new root must be private")
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.root); err != nil {
		t.Fatal("disable must not remove cache directories")
	}
}

func TestArchiveGuardNativePartialChangesAreRecoverable(t *testing.T) {
	for _, failAt := range []int{1, 2, 4, 7} {
		t.Run(fmt.Sprintf("enable-%d", failAt), func(t *testing.T) {
			f := newArchiveFixture(t, true)
			plan, err := PlanArchiveGuard(f.home)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			plan.runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "/bin/chmod" {
					calls++
					if calls == failAt {
						return nil, errors.New("injected ACL mutation failure")
					}
				}
				return archiveRun(ctx, name, args...)
			}
			result, err := EnableArchiveGuard(plan, f.state)
			if err == nil || !result.RecoveryNeeded {
				t.Fatalf("failed enable not recoverable: %+v %v", result, err)
			}
			if _, err := EnableArchiveGuard(plan, f.state); err == nil {
				t.Fatal("partial enable silently adopted")
			}
			if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
				t.Fatal(err)
			}
			nodes, _, err := archiveLayout(f.home)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range nodes {
				if len(archiveTestACL(t, filepath.Join(f.root, node.Path))) != 0 {
					t.Fatalf("ACL left behind after partial rollback: %s", node.Path)
				}
			}
		})
	}
	t.Run("failure-after-mutation", func(t *testing.T) {
		f := newArchiveFixture(t, false)
		plan, err := PlanArchiveGuard(f.home)
		if err != nil {
			t.Fatal(err)
		}
		plan.runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			output, err := archiveRun(ctx, name, args...)
			if name == "/bin/chmod" && err == nil {
				return nil, errors.New("injected failure after successful mutation")
			}
			return output, err
		}
		if _, err := EnableArchiveGuard(plan, f.state); err == nil {
			t.Fatal("expected uncertain mutation result")
		}
		if len(archiveTestACL(t, f.root)) != 1 {
			t.Fatal("fixture did not perform mutation")
		}
		if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
			t.Fatal("write-ahead receipt did not recover uncertain result", err)
		}
		if len(archiveTestACL(t, f.root)) != 0 {
			t.Fatal("uncertain mutation survived rollback")
		}
	})
	t.Run("disable", func(t *testing.T) {
		f := newArchiveFixture(t, true)
		f.enable(t)
		if err := os.Mkdir(filepath.Join(f.root, "abcdefabcdef"), 0700); err != nil {
			t.Fatal(err)
		}
		calls := 0
		run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "/bin/chmod" {
				calls++
				if calls == 2 {
					return nil, errors.New("injected disable failure")
				}
			}
			return archiveRun(ctx, name, args...)
		}
		result, err := disableArchiveGuard(f.home, f.state, run)
		if err == nil || !result.RecoveryNeeded || len(archiveTestACL(t, f.root)) != 0 {
			t.Fatalf("root inheritance was not removed first: %+v %v", result, err)
		}
		if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
			t.Fatal(err)
		}
		if len(archiveTestACL(t, filepath.Join(f.root, "abcdefabcdef"))) != 0 {
			t.Fatal("new child was not recovered")
		}
	})
}

func TestArchiveGuardPreflightRejectsUnsafeLayouts(t *testing.T) {
	for _, scenario := range []string{"home-link", "zcode-link", "v2-link", "root-link", "workspace-link", "pending-link", "artifact-link", "hardlink", "legacy", "unexpected-root", "unexpected-subdir", "artifact-not-directory", "too-many"} {
		t.Run(scenario, func(t *testing.T) {
			f := newArchiveFixture(t, true)
			if strings.HasSuffix(scenario, "-link") {
				path := map[string]string{"home-link": f.home, "zcode-link": filepath.Join(f.home, ".zcode"), "v2-link": filepath.Join(f.home, ".zcode", "v2"), "root-link": f.root, "workspace-link": f.workspace, "pending-link": filepath.Join(f.workspace, "pending"), "artifact-link": filepath.Join(f.workspace, "pending", "work.tar.gz.enc")}[scenario]
				if scenario == "home-link" {
					alias := filepath.Join(f.home, "home-alias")
					if err := os.Symlink(f.home, alias); err != nil {
						t.Fatal(err)
					}
					f.home = alias
				} else {
					target := filepath.Join(f.home, "displaced")
					if err := os.Rename(path, target); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				switch scenario {
				case "hardlink":
					if err := os.Link(filepath.Join(f.workspace, "pending", "work.tar.gz.enc"), filepath.Join(f.home, "alias")); err != nil {
						t.Fatal(err)
					}
				case "legacy":
					if err := os.Mkdir(filepath.Join(f.home, ".zcode", "v2", "repo-snapshots"), 0700); err != nil {
						t.Fatal(err)
					}
				case "unexpected-root", "unexpected-subdir":
					parent := f.root
					if scenario == "unexpected-subdir" {
						parent = f.workspace
					}
					if err := os.Mkdir(filepath.Join(parent, "unknown"), 0700); err != nil {
						t.Fatal(err)
					}
				case "artifact-not-directory":
					if err := os.Rename(filepath.Join(f.workspace, "tmp"), filepath.Join(f.home, "old-tmp")); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(f.workspace, "tmp"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				case "too-many":
					for i := 0; i < archiveGuardLimit; i++ {
						if err := os.WriteFile(filepath.Join(f.workspace, fmt.Sprintf("checkpoint-%d.json", i)), nil, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if _, err := PlanArchiveGuard(f.home); err == nil {
				t.Fatal("unsafe layout accepted")
			}
		})
	}
}

func TestArchiveGuardNativeDetectsBypassesWithoutClaimingProtection(t *testing.T) {
	for _, bypass := range []string{"symlink", "import"} {
		t.Run(bypass, func(t *testing.T) {
			f := newArchiveFixture(t, false)
			f.enable(t)
			outside := filepath.Join(f.home, "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if bypass == "symlink" {
				if err := os.Symlink(outside, filepath.Join(f.workspace, "pending")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.workspace, "pending", "bypass.enc"), []byte("synthetic"), 0600); err != nil {
					t.Fatal("expected documented symlink bypass", err)
				}
			} else {
				if err := os.Rename(outside, filepath.Join(f.root, "abcdefabcdef")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(f.root, "abcdefabcdef", "tmp"), 0700); err != nil {
					t.Fatal("expected documented imported-directory bypass", err)
				}
			}
			status, err := InspectArchiveGuard(f.home, f.state)
			if err == nil || status.Healthy || status.Enabled || len(status.Problems) == 0 {
				t.Fatalf("bypass was reported protected: %+v %v", status, err)
			}
			if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
				t.Fatal(err)
			}
			if len(archiveTestACL(t, f.root)) != 0 || len(archiveTestACL(t, f.workspace)) != 0 {
				t.Fatal("unsupported new entry prevented owned ACL rollback")
			}
			if bypass == "symlink" {
				if target, err := os.Readlink(filepath.Join(f.workspace, "pending")); err != nil || target != outside {
					t.Fatal("rollback edited foreign symlink", err)
				}
				if data, err := os.ReadFile(filepath.Join(outside, "bypass.enc")); err != nil || string(data) != "synthetic" || len(archiveTestACL(t, outside)) != 0 {
					t.Fatal("rollback changed external data or ACLs", err)
				}
			}
		})
	}
}

func TestArchiveGuardDisableStopsInheritanceBeforeRefusingKnownAlias(t *testing.T) {
	f := newArchiveFixture(t, true)
	f.enable(t)
	old := filepath.Join(f.workspace, "pending", "work.tar.gz.enc")
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	// The parent denies creation, so explicitly remove only the test fixture's
	// own parent entry to construct the unsupported replacement.
	archiveTestCommand(t, "-a#", "0", filepath.Dir(old))
	external := filepath.Join(f.home, "external-file")
	if err := os.WriteFile(external, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, old); err != nil {
		t.Fatal(err)
	}
	archiveTestCommand(t, "+a#", "0", archiveRule(f.principal, "artifact-dir"), filepath.Dir(old))
	result, err := DisableArchiveGuard(f.home, f.state)
	if err == nil || !result.RecoveryNeeded {
		t.Fatalf("known alias was not refused: %+v %v", result, err)
	}
	if len(archiveTestACL(t, f.root)) != 0 || len(archiveTestACL(t, f.workspace)) != 0 {
		t.Fatal("root and workspace inheritance was not stopped before known alias refusal")
	}
	if data, err := os.ReadFile(external); err != nil || string(data) != "unchanged" || len(archiveTestACL(t, external)) != 0 {
		t.Fatal("known alias target changed", err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal("remaining owned ACLs did not recover", err)
	}
}

func TestArchiveGuardDisableRestoresUnexpectedDirectChildren(t *testing.T) {
	f := newArchiveFixture(t, false)
	f.enable(t)
	for _, name := range []string{"unexpected", ".hidden-cache", "unexpected with spaces"} {
		path := filepath.Join(f.root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		archiveWantDenied(t, os.Mkdir(filepath.Join(path, "child"), 0700))
	}
	foreign := filepath.Join(f.home, "foreign")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	archiveTestCommand(t, "+a#", "0", f.principal+" allow readattr", foreign)
	foreignACL := archiveTestACL(t, foreign)
	// Importing retains the foreign ACL and does not acquire root inheritance.
	imported := filepath.Join(f.root, "foreign-import")
	if err := os.Rename(foreign, imported); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(f.root, "alias")
	if err := os.Symlink(imported, alias); err != nil {
		t.Fatal(err)
	}
	status, err := InspectArchiveGuard(f.home, f.state)
	if err == nil || status.Healthy {
		t.Fatal("unexpected layout must degrade status")
	}
	// Fail after the receipt has adopted non-hash inherited children; resuming
	// must accept that narrow receipt shape without expanding mutation scope.
	mutations := 0
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/chmod" {
			mutations++
			if mutations == 2 {
				return nil, errors.New("interrupt after inherited adoption")
			}
		}
		return archiveRun(ctx, name, args...)
	}
	if result, err := disableArchiveGuard(f.home, f.state, run); err == nil || !result.RecoveryNeeded {
		t.Fatalf("expected recoverable interruption: %+v %v", result, err)
	}
	if _, err := archiveLoad(filepath.Join(f.state, "archive-guard"), f.home, f.principal); err != nil {
		t.Fatal("unexpected-name recovery receipt rejected", err)
	}
	if result, err := DisableArchiveGuard(f.home, f.state); err != nil || result.RecoveryNeeded {
		t.Fatalf("unexpected child rollback failed: %+v %v", result, err)
	}
	for _, name := range []string{"unexpected", ".hidden-cache", "unexpected with spaces"} {
		path := filepath.Join(f.root, name)
		if len(archiveTestACL(t, path)) != 0 {
			t.Fatal("owned inherited restriction left behind", name)
		}
		if err := os.Mkdir(filepath.Join(path, "child"), 0700); err != nil {
			t.Fatal("unexpected child still restricted", err)
		}
	}
	if !reflect.DeepEqual(foreignACL, archiveTestACL(t, imported)) {
		t.Fatal("foreign ACL was changed")
	}
	if target, err := os.Readlink(alias); err != nil || target != imported {
		t.Fatal("foreign symlink was changed", err)
	}
}

func TestArchiveGuardDisableRefusesAmbiguousUnexpectedChild(t *testing.T) {
	f := newArchiveFixture(t, false)
	f.enable(t)
	path := filepath.Join(f.root, "unexpected")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	archiveTestCommand(t, "+a#", "0", f.principal+" allow readattr", path)
	before := archiveTestACL(t, path)
	result, err := DisableArchiveGuard(f.home, f.state)
	if err == nil || !result.RecoveryNeeded || !reflect.DeepEqual(before, archiveTestACL(t, path)) {
		t.Fatalf("ambiguous child was edited or ignored: %+v %v", result, err)
	}
	if _, err := archiveLoad(filepath.Join(f.state, "archive-guard"), f.home, f.principal); err != nil {
		t.Fatal("ambiguous ownership lost recovery receipt", err)
	}
	archiveTestCommand(t, "-a#", "0", path)
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal("restored unexpected child could not recover", err)
	}
	if len(archiveTestACL(t, path)) != 0 {
		t.Fatal("owned inherited entry survived recovery")
	}
}

func TestArchiveGuardDisableRetiresAbsentRecordsBeforeJournalingNewChildren(t *testing.T) {
	f := newArchiveFixture(t, false)
	f.enable(t)
	dir := filepath.Join(f.state, "archive-guard")
	receipt, err := archiveLoad(dir, f.home, f.principal)
	if err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(f.home, "old-artifact-metadata")
	if err := os.WriteFile(oldFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	node, err := archiveMetadata(oldFile, "unused", "artifact-file")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldFile); err != nil {
		t.Fatal(err)
	}
	// Model a full, valid historical journal after its artifacts were removed.
	// No file content or 1,022 unnecessary native ACL mutations are needed.
	for len(receipt.Changes) < archiveGuardLimit {
		node.Path = fmt.Sprintf("0123456789ab/tmp/removed-%04d", len(receipt.Changes))
		receipt.Changes = append(receipt.Changes, archiveChange{archiveNode: node, Rule: archiveRule(f.principal, node.Kind), Before: []string{}})
	}
	if err := archiveSave(dir, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveLoad(dir, f.home, f.principal); err != nil {
		t.Fatal("historical journal is invalid", err)
	}
	newHash := filepath.Join(f.root, "abcdefabcdef")
	if err := os.Mkdir(newHash, 0700); err != nil {
		t.Fatal(err)
	}
	mutations := 0
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/chmod" {
			mutations++
			if mutations == 2 {
				return nil, errors.New("interruption after inherited child journal update")
			}
		}
		return archiveRun(ctx, name, args...)
	}
	if result, err := disableArchiveGuard(f.home, f.state, run); err == nil || !result.RecoveryNeeded {
		t.Fatalf("expected recoverable interruption: %+v %v", result, err)
	}
	recovered, err := archiveLoad(dir, f.home, f.principal)
	if err != nil || len(recovered.Changes) != 3 {
		t.Fatalf("interruption left unreadable or oversized journal: %v", err)
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal("bounded journal failed to recover", err)
	}
	if len(archiveTestACL(t, newHash)) != 0 {
		t.Fatal("new inherited entry survived recovery")
	}
}

func TestArchiveGuardNativeRefusesExternalACLChanges(t *testing.T) {
	for _, change := range []string{"duplicate-root", "insert-child", "replace-inode", "preexisting-inheritance"} {
		t.Run(change, func(t *testing.T) {
			f := newArchiveFixture(t, false)
			if change == "preexisting-inheritance" {
				archiveTestCommand(t, "+a#", "0", f.principal+" deny writeextattr,directory_inherit,limit_inherit,only_inherit", f.root)
				before := archiveTestACL(t, f.root)
				plan, err := PlanArchiveGuard(f.home)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := EnableArchiveGuard(plan, f.state); err == nil {
					t.Fatal("ambiguous inheritance accepted")
				}
				if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, archiveTestACL(t, f.root)) {
					t.Fatal("preexisting inheritance changed")
				}
				return
			}
			f.enable(t)
			switch change {
			case "duplicate-root":
				archiveTestCommand(t, "+a#", "0", archiveRule(f.principal, "root"), f.root)
			case "insert-child":
				archiveTestCommand(t, "+a#", "0", f.principal+" allow readattr", f.workspace)
			case "replace-inode":
				archiveTestCommand(t, "-a#", "0", f.workspace)
				if err := os.Rename(f.workspace, filepath.Join(f.home, "old-hash")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.workspace, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := archiveTestACL(t, f.workspace)
			result, err := DisableArchiveGuard(f.home, f.state)
			if err == nil || !result.RecoveryNeeded {
				t.Fatalf("external change not refused: %+v %v", result, err)
			}
			if !reflect.DeepEqual(before, archiveTestACL(t, f.workspace)) {
				t.Fatal("foreign ACL modified")
			}
			switch change {
			case "duplicate-root":
				archiveTestCommand(t, "-a#", "0", f.root)
			case "insert-child":
				archiveTestCommand(t, "-a#", "0", f.workspace)
			case "replace-inode":
				if err := os.Remove(f.workspace); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(f.home, "old-hash"), f.workspace); err != nil {
					t.Fatal(err)
				}
				archiveTestCommand(t, "+a#", "0", archiveRule(f.principal, "workspace"), f.workspace)
			}
			if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
				t.Fatal("failed to recover after restoring external edits", err)
			}
		})
	}
}

func TestArchiveGuardRejectsInvalidReceiptsAndOutsideState(t *testing.T) {
	mutations := map[string]func(*archiveReceipt){
		"version":   func(r *archiveReceipt) { r.Version = 99 },
		"home":      func(r *archiveReceipt) { r.Home += "/elsewhere" },
		"principal": func(r *archiveReceipt) { r.Principal = "user:root" },
		"outside":   func(r *archiveReceipt) { r.Changes[0].Path = "../../outside" },
		"absolute":  func(r *archiveReceipt) { r.Changes[0].Path = "/tmp/outside" },
		"rule":      func(r *archiveReceipt) { r.Changes[0].Rule = r.Principal + " deny read" },
		"duplicate": func(r *archiveReceipt) { r.Changes = append(r.Changes, r.Changes[0]) },
		"phase":     func(r *archiveReceipt) { r.Phase = "unknown" },
		"empty":     func(r *archiveReceipt) { r.Changes = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newArchiveFixture(t, false)
			f.enable(t)
			dir := filepath.Join(f.state, "archive-guard")
			receipt, err := archiveLoad(dir, f.home, f.principal)
			if err != nil {
				t.Fatal(err)
			}
			mutate(receipt)
			if err := archiveSave(dir, receipt); err != nil {
				t.Fatal(err)
			}
			before := archiveTestACL(t, f.root)
			if _, err := DisableArchiveGuard(f.home, f.state); err == nil {
				t.Fatal("malformed receipt accepted")
			}
			if !reflect.DeepEqual(before, archiveTestACL(t, f.root)) {
				t.Fatal("ACL mutated before receipt validation")
			}
		})
	}
	t.Run("state-path", func(t *testing.T) {
		f := newArchiveFixture(t, false)
		plan, err := PlanArchiveGuard(f.home)
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{filepath.Dir(f.home), f.home, filepath.Join(f.home, ".zcode", "state"), f.home + "/../escape"} {
			if _, err := EnableArchiveGuard(plan, state); err == nil {
				t.Fatalf("unsafe state path accepted: %s", state)
			}
		}
		alias := filepath.Join(f.home, "state-alias")
		if err := os.Symlink(f.root, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := EnableArchiveGuard(plan, alias); err == nil {
			t.Fatal("state symlink accepted")
		}
	})
	t.Run("lock-and-private-receipt", func(t *testing.T) {
		f := newArchiveFixture(t, false)
		f.enable(t)
		dir := filepath.Join(f.state, "archive-guard")
		release, err := AcquireLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DisableArchiveGuard(f.home, f.state); err == nil {
			t.Fatal("concurrent manager lock ignored")
		}
		release()
		path := filepath.Join(dir, "receipt.json")
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), "synthetic fixture") {
			t.Fatal("receipt read client content")
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := DisableArchiveGuard(f.home, f.state); err == nil {
			t.Fatal("public receipt accepted")
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
			t.Fatal(err)
		}
	})
}
