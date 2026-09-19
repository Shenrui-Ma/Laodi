package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func updateTestExecutable(t *testing.T, installed bool) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		dir = filepath.Join(dir, "state with spaces", "runtime")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".distribution.json"), []byte(`{"Version":1,"Files":{"laodi":"test-digest"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(dir, "laodi")
	if err := os.WriteFile(executable, []byte("fake executable"), 0700); err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestUpdateDownloadsOfficialBootstrapAndPreservesArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell bootstrap; Windows downloader tested separately")
	}
	executable := updateTestExecutable(t, true)
	state := filepath.Dir(filepath.Dir(executable))
	var script string
	var calls int
	commands := updateCommands{
		platform:   "darwin",
		executable: func() (string, error) { return executable, nil },
		run: func(ctx context.Context, name string, args ...string) error {
			calls++
			if calls == 1 {
				if name != "/usr/bin/curl" {
					t.Fatalf("download executable = %q", name)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("download has no deadline")
				}
				script = args[len(args)-2]
				want := []string{"-q", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--connect-timeout", "15", "--max-time", "60", "--max-filesize", "1048576", "--output", script, updateBootstrapURL}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("download args = %q", args)
				}
				for _, check := range []struct {
					path string
					mode os.FileMode
				}{{filepath.Dir(script), 0700}, {script, 0600}} {
					info, err := os.Stat(check.path)
					if err != nil || info.Mode().Perm() != check.mode {
						t.Fatalf("private download path: %v, %v", info, err)
					}
				}
				return os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0600)
			}
			want := []string{script, "--version", "v0.3.0-preview.2", "--dry-run", "--no-notifications", "--state-dir", state, "--format", "json"}
			if name != "/bin/sh" || !reflect.DeepEqual(args, want) {
				t.Fatalf("install command = %q %q", name, args)
			}
			return nil
		},
	}
	if err := runUpdateWith(context.Background(), []string{"--format", "json", "--dry-run", "--no-notifications", "--version", "v0.3.0-preview.2"}, commands); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
	if _, err := os.Stat(filepath.Dir(script)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("download temporary directory remains: %v", err)
	}
}

func TestUpdateExplicitStatePathIsOneArgument(t *testing.T) {
	state := filepath.Join(t.TempDir(), "$(touch never); 'state'")
	var shellArgs []string
	commands := updateCommands{platform: "darwin", executable: func() (string, error) {
		t.Fatal("explicit state directory should not infer executable location")
		return "", nil
	}, run: func(_ context.Context, name string, args ...string) error {
		if name == "/usr/bin/curl" {
			return os.WriteFile(args[len(args)-2], []byte("fake bootstrap"), 0600)
		}
		shellArgs = append([]string(nil), args...)
		return nil
	}}
	if err := runUpdateWith(context.Background(), []string{"--state-dir", state}, commands); err != nil {
		t.Fatal(err)
	}
	if len(shellArgs) != 5 || shellArgs[1] != "--state-dir" || shellArgs[2] != state || shellArgs[3] != "--format" || shellArgs[4] != "text" {
		t.Fatalf("unexpected script arguments: %q", shellArgs)
	}
}

func TestUpdateRejectsInvalidOptionsBeforeNetwork(t *testing.T) {
	for _, args := range [][]string{
		{"--version", "../../bad"}, {"--version", "v1.2.3;echo"}, {"--version", "v01.2.3"},
		{"--version", "v1.2.3-preview.01"}, {"--version", strings.Repeat("v", 129)},
		{"--format", "yaml"}, {"--source-dir", "/tmp"}, {"extra"}, {"--state-dir", "/tmp/\ninvalid"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			commands := updateCommands{platform: "darwin", run: func(context.Context, string, ...string) error {
				t.Fatal("unexpected network or install")
				return nil
			}}
			if err := runUpdateWith(context.Background(), args, commands); err == nil {
				t.Fatal("expected invalid argument error")
			}
		})
	}
}

func TestUpdateHelpDoesNotDownload(t *testing.T) {
	if err := runUpdateWith(context.Background(), []string{"--help"}, updateCommands{}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"update", "--help"}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRejectsUnsupportedPlatform(t *testing.T) {
	if err := runUpdateWith(context.Background(), nil, updateCommands{platform: "linux"}); err == nil {
		t.Fatal("expected platform error")
	}
}

func TestUpdateFailureCleansTemporaryFiles(t *testing.T) {
	for _, failure := range []string{"download", "empty", "oversized", "symlink", "installer"} {
		t.Run(failure, func(t *testing.T) {
			executable := updateTestExecutable(t, false)
			var script string
			calls := 0
			sentinel := errors.New("simulated failure")
			commands := updateCommands{platform: "darwin", executable: func() (string, error) { return executable, nil }, run: func(_ context.Context, name string, args ...string) error {
				calls++
				if name == "/bin/sh" {
					if failure != "installer" {
						t.Fatal("invalid bootstrap executed")
					}
					return sentinel
				}
				script = args[len(args)-2]
				switch failure {
				case "download":
					return sentinel
				case "empty":
					return nil
				case "oversized":
					return os.WriteFile(script, make([]byte, maxUpdateBootstrap+1), 0600)
				case "symlink":
					if err := os.Remove(script); err != nil {
						return err
					}
					return os.Symlink(executable, script)
				default:
					return os.WriteFile(script, []byte("fake bootstrap"), 0600)
				}
			}}
			err := runUpdateWith(context.Background(), nil, commands)
			if err == nil {
				t.Fatal("expected failure")
			}
			if (failure == "download" || failure == "installer") && !errors.Is(err, sentinel) {
				t.Fatalf("underlying error lost: %v", err)
			}
			wantCalls := 1
			if failure == "installer" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls = %d", calls)
			}
			if _, err := os.Stat(filepath.Dir(script)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary directory remains: %v", err)
			}
		})
	}
}

func TestUpdateStateDirectoryInference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launcher symlink; Windows routing tested separately")
	}
	executable := updateTestExecutable(t, true)
	link := filepath.Join(t.TempDir(), "laodi-link")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	state, err := installedUpdateStateDir(link)
	if err != nil || state != filepath.Dir(filepath.Dir(executable)) {
		t.Fatalf("state = %q, error = %v", state, err)
	}
	standalone := updateTestExecutable(t, false)
	if state, err := installedUpdateStateDir(standalone); err != nil || state != "" {
		t.Fatalf("standalone state = %q, error = %v", state, err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(executable), ".distribution.json"), []byte(`{"Version":1,"Files":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installedUpdateStateDir(executable); err == nil {
		t.Fatal("expected invalid receipt error")
	}
}
