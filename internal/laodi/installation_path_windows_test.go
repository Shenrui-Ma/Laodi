//go:build windows

package laodi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsInstallationPathDetectsMappedHandle(t *testing.T) {
	actualDir := t.TempDir()
	file, err := os.Open(actualDir)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	actual, err := windowsFinalHandlePath(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWindowsInstallationDirectoryHandle(actual, file); err != nil {
		t.Fatal(err)
	}
	logical := filepath.Join(filepath.Dir(actual), "different-logical-install")
	if err := verifyWindowsInstallationDirectoryHandle(logical, file); !errors.Is(err, errWindowsInstallPathRedirected) || !strings.Contains(err.Error(), "no installation root was changed") {
		t.Fatalf("mapped handle was accepted or unexplained: %v", err)
	}
	if !sameWindowsInstallationPath(strings.ToUpper(actual), actual) || sameWindowsInstallationPath(actual+".", actual) || sameWindowsInstallationPath(`\\server\share\state`, actual) {
		t.Fatal("final path comparison accepted aliases or rejected case identity")
	}
}

func TestWindowsInstallationPathPlanningDoesNotCreateOrFollowJunctions(t *testing.T) {
	parent := t.TempDir()
	file, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := windowsFinalHandlePath(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(actual, "not-created", "state")
	if err := verifyWindowsInstallationPath(missing); err != nil {
		t.Fatal(err)
	}
	if err := verifyWindowsInstallationStatePath(missing); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("path planning created state")
	}
	alias := filepath.Join(actual, "junction")
	if err := createUnsafeTestLink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	if err := verifyWindowsInstallationPath(filepath.Join(alias, "state")); err == nil {
		t.Fatal("installation accepted a junction ancestor")
	}
}

func TestWindowsInstallationStateChecksExistingEntryIdentities(t *testing.T) {
	for _, name := range []string{windowsCurrentName, "laodi.exe", "laodi-host.exe"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "unrelated")
			if err := os.WriteFile(outside, []byte("synthetic entry"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(outside, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			if err := verifyWindowsInstallationPath(dir); err != nil {
				t.Fatal("fixture directory should be a real native path", err)
			}
			if err := verifyWindowsInstallationStatePath(dir); err == nil {
				t.Fatal("real directory bypassed entry identity check")
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := verifyWindowsInstallationRoot(root); err == nil {
				t.Fatal("bound root bypassed entry identity check")
			}
		})
	}
}

// This opt-in check reads handle metadata only. It does not install, repair,
// delete, enumerate contents, or alter the sandbox's logical/physical mapping.
func TestWindowsActualInstallationPathMapping(t *testing.T) {
	requested := os.Getenv("LAODI_TEST_INSTALL_PATH")
	if requested == "" {
		t.Skip("set LAODI_TEST_INSTALL_PATH for native final-handle mapping verification")
	}
	file, err := os.Open(requested)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	actual, err := windowsFinalHandlePath(file)
	if err != nil {
		t.Fatal(err)
	}
	redirected := !sameWindowsInstallationPath(requested, actual)
	for _, name := range []string{windowsCurrentName, "laodi.exe", "laodi-host.exe"} {
		path := filepath.Join(requested, name)
		entry, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		entryPath, err := windowsFinalHandlePath(entry)
		entry.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !sameWindowsInstallationPath(path, entryPath) {
			redirected = true
			t.Logf("entry redirection detected: %s", name)
		}
	}
	err = verifyWindowsInstallationStatePath(requested)
	if !redirected {
		if err != nil {
			t.Fatal(err)
		}
		t.Log("requested installation path is visible at the same native handle path")
	} else {
		if sameWindowsInstallationPath(requested, actual) {
			t.Log("real native directory contains virtualized entry files")
		}
		if !errors.Is(err, errWindowsInstallPathRedirected) {
			t.Fatalf("native redirection was not rejected: %v", err)
		}
		root, err := os.OpenRoot(requested)
		if err != nil {
			t.Fatal(err)
		}
		err = verifyWindowsInstallationRoot(root)
		root.Close()
		if !errors.Is(err, errWindowsInstallPathRedirected) {
			t.Fatalf("bound installation root bypassed redirection check: %v", err)
		}
		source, _ := windowsPayloadFixture(t, "v0.4.0-mapping-test")
		if _, err := PlanDistribution(DistributionOptions{SourceDir: source, StateDir: requested, AppCandidates: []string{}}); !errors.Is(err, errWindowsInstallPathRedirected) {
			t.Fatalf("installation planner accepted virtualized root: %v", err)
		}
		if _, err := PlanHookConfig(HookConfigOptions{Adapter: "zcode", StateDir: requested, Home: t.TempDir()}); !errors.Is(err, errWindowsInstallPathRedirected) {
			t.Fatalf("direct hook planner bypassed virtualized state rejection: %v", err)
		}
		if err := verifyHookInstallState(HookConfigPlan{options: HookConfigOptions{StateDir: requested}}); !errors.Is(err, errWindowsInstallPathRedirected) {
			t.Fatalf("post-create hook state check bypassed redirection: %v", err)
		}
		if _, err := ManageWindowsNotifications(context.Background(), requested, "enable"); !errors.Is(err, errWindowsInstallPathRedirected) {
			t.Fatalf("direct notification enable bypassed virtualized state rejection: %v", err)
		}
		t.Log("native installation path redirection detected and rejected")
	}
}
