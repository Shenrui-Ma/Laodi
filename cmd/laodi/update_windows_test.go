package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWindowsUpdateRunsDownloadedInstallerBeforeCleanup(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "installer-failure"}[failed], func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state with spaces & literal")
			source := filepath.Join(t.TempDir(), "verified new installer")
			failure := errors.New("synthetic new installer failure")
			cleaned, ran := false, false
			commands := windowsUpdateCommands{
				download: func(_ context.Context, tag, stage string) (string, func(), error) {
					if tag != "v0.4.1-windows.beta.2" || stage != filepath.Join(state, "downloads") {
						t.Fatalf("unexpected download: %q %q", tag, stage)
					}
					return source, func() { cleaned = true }, nil
				},
				run: func(_ context.Context, executable string, args ...string) error {
					ran = true
					if cleaned {
						t.Fatal("download removed before the new installer completed")
					}
					want := []string{"install", "--source-dir", source, "--state-dir", state, "--format", "json", "--dry-run", "--no-notifications"}
					if executable != filepath.Join(source, "laodi.exe") || !reflect.DeepEqual(args, want) {
						t.Fatalf("new installer invocation: %q %q", executable, args)
					}
					if failed {
						return failure
					}
					return nil
				},
			}
			err := runWindowsUpdateWith(context.Background(), []string{"--version", "v0.4.1-windows.beta.2", "--state-dir", state, "--format", "json", "--dry-run", "--no-notifications"}, commands)
			if failed && !errors.Is(err, failure) || !failed && err != nil {
				t.Fatalf("installer outcome lost: %v", err)
			}
			if !ran || !cleaned {
				t.Fatalf("ran=%t cleaned=%t", ran, cleaned)
			}
		})
	}
}

func TestWindowsUpdateDoesNotExecuteFailedDownload(t *testing.T) {
	failure := errors.New("synthetic package verification failure")
	commands := windowsUpdateCommands{
		download: func(context.Context, string, string) (string, func(), error) {
			return "", nil, failure
		},
		run: func(context.Context, string, ...string) error {
			t.Fatal("unverified download executed")
			return nil
		},
	}
	err := runWindowsUpdateWith(context.Background(), []string{"--version", "v0.4.1-windows.beta.2", "--state-dir", t.TempDir()}, commands)
	if !errors.Is(err, failure) {
		t.Fatalf("download outcome lost: %v", err)
	}
}

func TestWindowsUpdateRejectsOptionsBeforeDownload(t *testing.T) {
	for _, args := range [][]string{nil, {"--version", "v1.2.3&echo"}, {"--version", "v1.2.3", "--format", "yaml"}, {"--version", "v1.2.3", "extra"}} {
		if err := runWindowsUpdateWith(context.Background(), args, windowsUpdateCommands{}); err == nil {
			t.Fatalf("invalid options accepted: %q", args)
		}
	}
	if err := runWindowsUpdateWith(context.Background(), []string{"--help"}, windowsUpdateCommands{}); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsUpdateInstallerUsesEOFAndPreservesExitError(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAODI_UPDATE_HELPER_TEST", "1")
	err = runWindowsUpdateInstaller(context.Background(), executable, "-test.run=^TestWindowsUpdateInstallerHelper$", "--", "literal spaces & pipe | percent %")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("helper EOF, argument forwarding or exit status failed: %v", err)
	}
}

func TestWindowsUpdateInstallerHelper(t *testing.T) {
	if os.Getenv("LAODI_UPDATE_HELPER_TEST") != "1" {
		return
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) != 0 {
		os.Exit(21)
	}
	if os.Args[len(os.Args)-1] != "literal spaces & pipe | percent %" {
		os.Exit(22)
	}
	os.Exit(23)
}
