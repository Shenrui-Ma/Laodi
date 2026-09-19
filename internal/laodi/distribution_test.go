package laodi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func distributionFixture(t *testing.T) DistributionOptions {
	t.Helper()
	if runtime.GOOS != "darwin" || os.Getuid() == 0 {
		t.Skip("user release installer supports macOS")
	}
	home := t.TempDir()
	source := filepath.Join(home, "download")
	files := map[string]string{"laodi": "synthetic executable; never executed", "LaodiNotify.app/Contents/Info.plist": "synthetic plist", "LaodiNotify.app/Contents/MacOS/LaodiNotify": "synthetic notifier; never executed", "skills/laodi/SKILL.md": "synthetic skill", "skills/laodi/references/usage.md": "synthetic usage"}
	for name, content := range files {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0600)
		if name == "laodi" || strings.HasSuffix(name, "/MacOS/LaodiNotify") {
			mode = 0700
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	return DistributionOptions{SourceDir: source, Home: home, AppCandidates: []string{}, LookPath: func(string) (string, error) { return "", errors.New("absent") }, RequestNotifications: true}
}
func offlineDistributionPlan(t *testing.T, options DistributionOptions) DistributionPlan {
	t.Helper()
	plan, err := PlanDistribution(options)
	if err != nil {
		t.Fatal(err)
	}
	plan.serviceRunner = func(context.Context, ...string) error { t.Fatal("unexpected launchctl invocation"); return nil }
	plan.notifierRunner = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unexpected notification invocation")
		return nil, nil
	}
	return plan
}
func fixtureTool(t *testing.T, options *DistributionOptions, tool string) {
	t.Helper()
	path := filepath.Join(options.Home, ".claude")
	if tool == "zcode" {
		path = filepath.Join(options.Home, ".zcode", "cli")
	}
	if tool == "app" {
		path = filepath.Join(options.Home, "Applications", "ZCode.app")
		options.AppCandidates = []string{path}
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if tool == "app" {
		if err := os.MkdirAll(filepath.Join(path, "Contents", "MacOS"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "Contents", "MacOS", "ZCode"), []byte("synthetic app; never executed"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "Contents", "Info.plist"), []byte("synthetic plist"), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	name := "claude"
	if tool == "zcode" {
		name = "zcode"
	}
	executable := filepath.Join(options.Home, "tools", name)
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("synthetic executable; never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := options.LookPath
	options.LookPath = func(candidate string) (string, error) {
		if candidate == name {
			return executable, nil
		}
		return previous(candidate)
	}
}

func TestDistributionPlanReadOnlyAndNoToolsDoesNotClaimMonitoring(t *testing.T) {
	options := distributionFixture(t)
	plan := offlineDistributionPlan(t, options)
	if _, err := os.Stat(plan.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plan created state")
	}
	if len(plan.Adapters) != 0 || !plan.HooksOnly || plan.FileCount != 5 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	result, err := InstallDistribution(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RuntimeInstalled || result.ServiceInstalled || len(result.HookAdapters) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("false protection claim: %+v", result)
	}
	receipt, err := os.ReadFile(filepath.Join(plan.RuntimeDir, distributionReceiptName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(receipt), options.SourceDir) || strings.Contains(string(receipt), "synthetic executable") {
		t.Fatal("receipt retained source path or contents")
	}
	for _, name := range []string{"laodi", "LaodiNotify.app/Contents/MacOS/LaodiNotify"} {
		info, err := os.Stat(filepath.Join(plan.RuntimeDir, filepath.FromSlash(name)))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("unsafe executable mode")
		}
	}
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal("identical install should be safe:", err)
	}
}

func TestDistributionConnectsBothToolsAndPermissionOnlyWhenUndecided(t *testing.T) {
	options := distributionFixture(t)
	fixtureTool(t, &options, "app")
	fixtureTool(t, &options, "claude-code")
	plan := offlineDistributionPlan(t, options)
	var calls []string
	plan.serviceRunner = func(ctx context.Context, args ...string) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded service call")
		}
		calls = append(calls, args[0])
		return nil
	}
	permission := "not_determined"
	var notificationCalls []string
	plan.notifierRunner = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if path != plan.Notifier {
			t.Fatal("source notifier used")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded notification call")
		}
		notificationCalls = append(notificationCalls, args[0])
		if args[0] == "--request-permission" {
			permission = "denied"
		}
		return json.Marshal(map[string]any{"ok": true, "authorization": permission})
	}
	result, err := InstallDistribution(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ServiceInstalled || result.NotificationStatus != "denied" || !reflect.DeepEqual(result.HookAdapters, []string{"zcode", "claude-code"}) {
		t.Fatalf("bad install result: %+v", result)
	}
	result, err = InstallDistribution(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"bootstrap", "print"}) {
		t.Fatalf("service restarted or unusual calls: %v", calls)
	}
	if !reflect.DeepEqual(notificationCalls, []string{"--status", "--request-permission", "--status"}) {
		t.Fatalf("repeated permission request: %v", notificationCalls)
	}
	for _, file := range []string{filepath.Join(options.Home, ".zcode", "cli", "config.json"), filepath.Join(options.Home, ".claude", "settings.json")} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), options.SourceDir) || !strings.Contains(string(data), plan.Executable) {
			t.Fatal("hook does not use stable runtime")
		}
	}
	// Ensure that removing registrations retains previously accumulated records.
	history := filepath.Join(plan.StateDir, "example-incident.json")
	if err := os.WriteFile(history, []byte("keep history"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	if calls[len(calls)-1] != "bootout" {
		t.Fatal("owned service was not stopped during explicit removal")
	}
	if data, err := os.ReadFile(history); err != nil || string(data) != "keep history" {
		t.Fatal("removal deleted history")
	}
	if _, err := os.Stat(plan.Executable); err != nil {
		t.Fatal("removal deleted runtime")
	}
}

func TestDistributionHooksOnlyAndNotificationOptOut(t *testing.T) {
	options := distributionFixture(t)
	fixtureTool(t, &options, "claude-code")
	options.RequestNotifications = false
	plan := offlineDistributionPlan(t, options)
	plan.serviceRunner = func(context.Context, ...string) error { return nil }
	result, err := InstallDistribution(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ServiceInstalled || !plan.HooksOnly || result.NotificationStatus != "not_requested" {
		t.Fatalf("wrong hooks-only result: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(options.Home, "Library", "LaunchAgents", serviceLabel+".plist"))
	if err != nil || !strings.Contains(string(data), "--hooks-only") {
		t.Fatal("hooks-only service not configured")
	}
}

func TestDistributionRejectsUnsafeOrForeignPayloads(t *testing.T) {
	for _, scenario := range []string{"source-symlink", "source-special", "missing-notifier", "foreign-runtime", "foreign-service", "foreign-hooks", "changed-runtime"} {
		t.Run(scenario, func(t *testing.T) {
			options := distributionFixture(t)
			state := filepath.Join(options.Home, "Library", "Application Support", "Laodi-skills")
			switch scenario {
			case "source-symlink":
				path := filepath.Join(options.SourceDir, "skills", "laodi", "other.md")
				if err := os.Symlink("SKILL.md", path); err != nil {
					t.Fatal(err)
				}
			case "source-special":
				if err := os.Mkdir(filepath.Join(options.SourceDir, "laodi-extra"), 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(options.SourceDir, "skills", "laodi", "bad")
				if err := os.Symlink("/dev/null", path); err != nil {
					t.Fatal(err)
				}
			case "missing-notifier":
				if err := os.Remove(filepath.Join(options.SourceDir, "LaodiNotify.app", "Contents", "MacOS", "LaodiNotify")); err != nil {
					t.Fatal(err)
				}
			case "foreign-runtime":
				if err := os.MkdirAll(filepath.Join(state, "runtime"), 0700); err != nil {
					t.Fatal(err)
				}
			case "foreign-service":
				path := filepath.Join(options.Home, "Library", "LaunchAgents", serviceLabel+".plist")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-hooks":
				if err := os.MkdirAll(state, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, "hook-install-zcode.json"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-source", "changed-runtime":
				plan := offlineDistributionPlan(t, options)
				if _, err := InstallDistribution(plan); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(options.SourceDir, "laodi")
				if scenario == "changed-runtime" {
					path = plan.Executable
				}
				if err := os.WriteFile(path, []byte("different payload"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := PlanDistribution(options); err == nil {
				t.Fatal("unsafe installation was accepted")
			}
		})
	}
}

func TestDistributionReportsPartialCompletionAndNeverRunsNotifierAfterServiceFailure(t *testing.T) {
	options := distributionFixture(t)
	fixtureTool(t, &options, "claude-code")
	plan := offlineDistributionPlan(t, options)
	plan.serviceRunner = func(context.Context, ...string) error { return errors.New("synthetic launchctl failure") }
	result, err := InstallDistribution(plan)
	if err == nil || !result.RuntimeInstalled || result.ServiceInstalled || !reflect.DeepEqual(result.HookAdapters, []string{"claude-code"}) {
		t.Fatalf("hidden partial completion: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(options.Home, ".claude", "settings.json")); err != nil {
		t.Fatal("completed hook phase was silently undone")
	}
	if _, err := os.Stat(filepath.Join(options.Home, "Library", "LaunchAgents", serviceLabel+".plist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed bootstrap left plist")
	}
}

func TestDistributionRejectsPlanTamperingBeforeWriting(t *testing.T) {
	options := distributionFixture(t)
	plan := offlineDistributionPlan(t, options)
	plan.Executable = "/tmp/foreign-executable"
	if _, err := InstallDistribution(plan); err == nil {
		t.Fatal("accepted changed destination")
	}
	if _, err := os.Stat(plan.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("mutated plan wrote state")
	}
}

func TestDistributionRemovalFromRuntimeAfterToolWasRemoved(t *testing.T) {
	options := distributionFixture(t)
	fixtureTool(t, &options, "app")
	options.RequestNotifications = false
	plan := offlineDistributionPlan(t, options)
	plan.serviceRunner = func(context.Context, ...string) error { return nil }
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(options.AppCandidates[0]); err != nil {
		t.Fatal(err)
	}
	options.SourceDir = plan.RuntimeDir
	options.Remove = true
	removal := offlineDistributionPlan(t, options)
	removal.serviceRunner = func(_ context.Context, args ...string) error {
		if args[0] != "bootout" {
			t.Fatalf("unexpected removal action: %v", args)
		}
		return nil
	}
	if _, err := InstallDistribution(removal); err == nil {
		t.Fatal("removal plan was installed")
	}
	if _, err := UninstallDistribution(removal); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plan.StateDir, serviceReceiptName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt survived explicit removal")
	}
}

func TestDistributionRefusesOldHookPathBeforeStartingService(t *testing.T) {
	options := distributionFixture(t)
	plan := offlineDistributionPlan(t, options)
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	fixtureTool(t, &options, "claude-code")
	old, err := PlanHookConfig(HookConfigOptions{Adapter: "claude-code", Executable: filepath.Join(options.SourceDir, "laodi"), Home: options.Home, StateDir: plan.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallHookConfig(old); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanDistribution(options); err == nil || !strings.Contains(err.Error(), "another executable") {
		t.Fatal("old source hook path was adopted", err)
	}
	if _, err := os.Stat(filepath.Join(options.Home, "Library", "LaunchAgents", serviceLabel+".plist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old path failure started service")
	}
}

func TestDistributionNotificationFailurePreservesInstalledResult(t *testing.T) {
	options := distributionFixture(t)
	fixtureTool(t, &options, "claude-code")
	plan := offlineDistributionPlan(t, options)
	plan.serviceRunner = func(context.Context, ...string) error { return nil }
	plan.notifierRunner = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("synthetic notification failure")
	}
	result, err := InstallDistribution(plan)
	if err != nil || !result.RuntimeInstalled || !result.ServiceInstalled || result.NotificationStatus != "unknown" || len(result.Warnings) < 2 {
		t.Fatalf("lost completed phases: %+v %v", result, err)
	}
}

func TestDistributionPermissionTimeoutRechecksWithoutRequestingAgain(t *testing.T) {
	for _, authorization := range []string{"authorized", "denied", "not_determined", "unknown"} {
		t.Run(authorization, func(t *testing.T) {
			var calls []string
			plan := DistributionPlan{Notifier: "/synthetic/LaodiNotify", RequestNotifications: true}
			plan.notifierRunner = func(ctx context.Context, _ string, args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] == "--request-permission" {
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) <= 60*time.Second {
						t.Fatal("installer would kill the helper before its 60-second permission timeout")
					}
					return nil, context.DeadlineExceeded
				}
				if len(calls) == 1 {
					return []byte(`{"ok":true,"authorization":"not_determined"}`), nil
				}
				if authorization == "unknown" {
					return nil, errors.New("synthetic status failure")
				}
				return json.Marshal(map[string]any{"ok": true, "authorization": authorization})
			}
			status, err := distributionNotifications(plan)
			if status != authorization || (err != nil) != (authorization == "unknown") {
				t.Fatalf("status=%s error=%v", status, err)
			}
			if !reflect.DeepEqual(calls, []string{"--status", "--request-permission", "--status"}) {
				t.Fatalf("must recheck once without another permission request: %v", calls)
			}
		})
	}
}

func TestDistributionUnavailableNotificationsExplainHowToContinue(t *testing.T) {
	for _, authorization := range []string{"authorized", "denied", "not_determined"} {
		t.Run(authorization, func(t *testing.T) {
			options := distributionFixture(t)
			fixtureTool(t, &options, "app")
			plan := offlineDistributionPlan(t, options)
			plan.serviceRunner = func(context.Context, ...string) error { return nil }
			plan.notifierRunner = func(context.Context, string, ...string) ([]byte, error) {
				return json.Marshal(map[string]any{"ok": true, "authorization": authorization})
			}
			result, err := InstallDistribution(plan)
			if err != nil || !result.ServiceInstalled || result.NotificationStatus != authorization {
				t.Fatalf("installation must retain its completed state: %+v %v", result, err)
			}
			guidance := strings.Join(result.Warnings, "\n")
			if authorization != "authorized" && !strings.Contains(guidance, "系统设置") {
				t.Fatal("missing recovery guidance for notification permission")
			}
			if authorization == "authorized" && strings.Contains(guidance, "系统设置") {
				t.Fatal("authorized users must not be told to enable notifications again")
			}
		})
	}
}

func TestDistributionDetectsExecutableWithoutReadingItsConfiguration(t *testing.T) {
	options := distributionFixture(t)
	options.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return filepath.Join(options.SourceDir, "laodi"), nil
		}
		return "", errors.New("absent")
	}
	plan := offlineDistributionPlan(t, options)
	if !reflect.DeepEqual(plan.Adapters, []string{"claude-code"}) || !plan.HooksOnly {
		t.Fatalf("PATH detection failed: %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(options.Home, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("detection created configuration")
	}
}

func TestDistributionRejectsDifferentHistoryBeforeAnyMutation(t *testing.T) {
	for _, hooksOnly := range []bool{false, true} {
		for _, installedRuntime := range []bool{false, true} {
			name := "combined"
			if hooksOnly {
				name = "hooks-only"
			}
			if installedRuntime {
				name += "-retained-runtime"
			} else {
				name += "-state-only"
			}
			t.Run(name, func(t *testing.T) {
				options := distributionFixture(t)
				options.RequestNotifications = false
				if hooksOnly {
					fixtureTool(t, &options, "claude-code")
				} else {
					fixtureTool(t, &options, "app")
				}
				plan := offlineDistributionPlan(t, options)
				if installedRuntime {
					plan.serviceRunner = func(context.Context, ...string) error { return nil }
					if _, err := InstallDistribution(plan); err != nil {
						t.Fatal(err)
					}
					if _, err := UninstallDistribution(plan); err != nil {
						t.Fatal(err)
					}
				}
				otherRoot := RootID("laodi:tool-hooks-only")
				if hooksOnly {
					otherRoot = RootID(filepath.Join(options.Home, ".zcode", "v2", "checkpoints"))
				}
				state := emptyState()
				state.Initialized = true
				state.RootID = otherRoot
				if err := SaveState(plan.StateDir, state); err != nil {
					t.Fatal(err)
				}
				statePath := filepath.Join(plan.StateDir, stateFileName)
				before, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				plan.serviceRunner = func(context.Context, ...string) error { calls++; return nil }
				if _, err := PlanDistribution(options); err == nil || !strings.Contains(err.Error(), "different evidence root") {
					t.Fatal("incompatible history accepted", err)
				}
				// A previously generated plan must also be rejected if history changed.
				result, err := InstallDistribution(plan)
				if err == nil || result.RuntimeInstalled || result.ServiceInstalled || len(result.HookAdapters) > 0 || calls != 0 {
					t.Fatalf("mismatched install changed registrations: %+v %v; calls=%d", result, err, calls)
				}
				after, err := os.ReadFile(statePath)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("incompatible history was modified")
				}
				if _, err := os.Stat(plan.RuntimeDir); installedRuntime && err != nil || !installedRuntime && !errors.Is(err, os.ErrNotExist) {
					t.Fatal("incompatible install changed runtime existence", err)
				}
				for _, path := range []string{filepath.Join(options.Home, "Library", "LaunchAgents", serviceLabel+".plist"), filepath.Join(plan.StateDir, serviceReceiptName), filepath.Join(plan.StateDir, "hook-install-zcode.json"), filepath.Join(plan.StateDir, "hook-install-claude-code.json")} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("incompatible install wrote registration", path, err)
					}
				}
			})
		}
	}
}

func TestDistributionAcceptsMatchingHistoryAndPreservesIt(t *testing.T) {
	for _, hooksOnly := range []bool{false, true} {
		name := "combined"
		if hooksOnly {
			name = "hooks-only"
		}
		t.Run(name, func(t *testing.T) {
			options := distributionFixture(t)
			options.RequestNotifications = false
			if hooksOnly {
				fixtureTool(t, &options, "claude-code")
			} else {
				fixtureTool(t, &options, "app")
			}
			stateDir := filepath.Join(options.Home, "Library", "Application Support", "Laodi-skills")
			state := emptyState()
			state.Initialized = true
			state.RootID = RootID(filepath.Join(options.Home, ".zcode", "v2", "checkpoints"))
			if hooksOnly {
				state.RootID = RootID("laodi:tool-hooks-only")
			}
			if err := SaveState(stateDir, state); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(stateDir, stateFileName))
			if err != nil {
				t.Fatal(err)
			}
			plan := offlineDistributionPlan(t, options)
			calls := 0
			plan.serviceRunner = func(context.Context, ...string) error { calls++; return nil }
			result, err := InstallDistribution(plan)
			if err != nil || !result.ServiceInstalled || calls != 1 {
				t.Fatalf("compatible history rejected: %+v %v; calls=%d", result, err, calls)
			}
			after, err := os.ReadFile(filepath.Join(stateDir, stateFileName))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("installer rewrote compatible history")
			}
		})
	}
}

func TestDistributionRemovalAllowsDifferentHistory(t *testing.T) {
	options := distributionFixture(t)
	options.RequestNotifications = false
	fixtureTool(t, &options, "app")
	plan := offlineDistributionPlan(t, options)
	plan.serviceRunner = func(context.Context, ...string) error { return nil }
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	state := emptyState()
	state.Initialized = true
	state.RootID = RootID("laodi:tool-hooks-only")
	if err := SaveState(plan.StateDir, state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(plan.StateDir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	options.Remove = true
	removal := offlineDistributionPlan(t, options)
	calls := 0
	removal.serviceRunner = func(_ context.Context, args ...string) error {
		if args[0] != "bootout" {
			t.Fatalf("unexpected removal action: %v", args)
		}
		calls++
		return nil
	}
	if _, err := UninstallDistribution(removal); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("legitimate removal did not stop owned service")
	}
	after, err := os.ReadFile(filepath.Join(plan.StateDir, stateFileName))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("removal rewrote incompatible history")
	}
}

func TestDistributionDoesNotEnrollResidualSettingsOrInvalidApp(t *testing.T) {
	for _, invalidApp := range []string{"absent", "empty-directory", "missing-plist", "non-executable"} {
		t.Run(invalidApp, func(t *testing.T) {
			options := distributionFixture(t)
			for _, directory := range []string{filepath.Join(options.Home, ".claude", "skills"), filepath.Join(options.Home, ".zcode", "cli")} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if invalidApp != "absent" {
				app := filepath.Join(options.Home, "Applications", "ZCode.app")
				options.AppCandidates = []string{app}
				if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0700); err != nil {
					t.Fatal(err)
				}
				if invalidApp == "missing-plist" {
					if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "ZCode"), []byte("synthetic"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if invalidApp == "non-executable" {
					if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte("synthetic"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "ZCode"), []byte("synthetic"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			plan := offlineDistributionPlan(t, options)
			if len(plan.Adapters) != 0 || plan.App != "" {
				t.Fatalf("residual tool was enrolled: %+v", plan)
			}
			result, err := InstallDistribution(plan)
			if err != nil || !result.RuntimeInstalled || result.ServiceInstalled || len(result.HookAdapters) != 0 || len(result.Warnings) == 0 {
				t.Fatalf("residual settings claimed a connected tool: %+v %v", result, err)
			}
			for _, path := range []string{filepath.Join(options.Home, ".claude", "settings.json"), filepath.Join(options.Home, ".zcode", "cli", "config.json"), filepath.Join(options.Home, "Library", "LaunchAgents", serviceLabel+".plist")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("residual settings were changed", path, err)
				}
			}
		})
	}
}

func TestDistributionRejectsChangedInstalledPermissions(t *testing.T) {
	cases := []struct {
		name, path string
		mode       os.FileMode
	}{
		{"runtime-directory", "", 0755},
		{"nested-directory", "LaodiNotify.app/Contents", 0755},
		{"cli-executable", "laodi", 0755},
		{"notification-executable", "LaodiNotify.app/Contents/MacOS/LaodiNotify", 0755},
		{"regular-file", "skills/laodi/SKILL.md", 0644},
		{"receipt", distributionReceiptName, 0644},
		{"setuid-executable", "laodi", 0700 | os.ModeSetuid},
		{"setgid-directory", "skills", 0700 | os.ModeSetgid},
		{"sticky-directory", "", 0700 | os.ModeSticky},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := distributionFixture(t)
			plan := offlineDistributionPlan(t, options)
			if _, err := InstallDistribution(plan); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(plan.RuntimeDir, filepath.FromSlash(tc.path))
			if err := os.Chmod(path, tc.mode); err != nil {
				if tc.mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
					t.Skip("filesystem does not allow requested special bits")
				}
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			special := tc.mode & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
			if special != 0 && before.Mode()&special != special {
				t.Skip("filesystem did not preserve requested special bits")
			}
			if _, err := PlanDistribution(options); err == nil {
				t.Fatal("changed private permissions accepted")
			}
			if _, err := InstallDistribution(plan); err == nil {
				t.Fatal("changed private permissions installed")
			}
			after, err := os.Lstat(path)
			if err != nil || before.Mode() != after.Mode() {
				t.Fatal("installer silently repaired or changed file permissions")
			}
		})
	}
}

func TestDistributionAcceptsPublicReleaseSourceModesButCopiesPrivately(t *testing.T) {
	options := distributionFixture(t)
	for _, file := range []string{"laodi", "LaodiNotify.app/Contents/MacOS/LaodiNotify"} {
		if err := os.Chmod(filepath.Join(options.SourceDir, filepath.FromSlash(file)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(options.SourceDir, "skills", "laodi", "SKILL.md"), 0644); err != nil {
		t.Fatal(err)
	}
	plan := offlineDistributionPlan(t, options)
	if _, err := InstallDistribution(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanDistribution(options); err != nil {
		t.Fatal("copied runtime was not private", err)
	}
}
