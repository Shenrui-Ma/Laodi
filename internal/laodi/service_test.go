package laodi

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func serviceFixture(t *testing.T) ServicePlan {
	t.Helper()
	if runtime.GOOS != "darwin" || os.Getuid() == 0 {
		t.Skip("LaunchAgent plans require an ordinary macOS user")
	}
	home := t.TempDir()
	executable := filepath.Join(home, "laodi <local> & safe")
	if err := os.WriteFile(executable, []byte("synthetic executable; never invoked"), 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanService(ServiceOptions{Home: home, Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	plan.runner = func(context.Context, ...string) error { t.Fatal("unconfigured fake runner"); return nil }
	return plan
}

func TestServicePlanIsReadOnlyAndPreservesArguments(t *testing.T) {
	plan := serviceFixture(t)
	entries, err := os.ReadDir(plan.options.Home)
	if err != nil || len(entries) != 1 {
		t.Fatal("planning created service/state files")
	}
	if strings.Contains(plan.Plist, "--build") || strings.Contains(plan.Plist, "sudo") {
		t.Fatal("default plan pins build or requests elevation")
	}
	decoder := xml.NewDecoder(strings.NewReader(plan.Plist))
	var arguments []string
	inArray := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("invalid plist XML: %v", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			if element.Name.Local == "array" {
				inArray = true
			}
			if inArray && element.Name.Local == "string" {
				var value string
				if err := decoder.DecodeElement(&value, &element); err != nil {
					t.Fatal(err)
				}
				arguments = append(arguments, value)
			}
		case xml.EndElement:
			if element.Name.Local == "array" {
				inArray = false
			}
		}
	}
	if !reflect.DeepEqual(arguments, plan.Arguments) || arguments[0] != plan.options.Executable || arguments[1] != "watch" {
		t.Fatalf("plist mangled argv: %v", arguments)
	}
	for _, fragment := range []string{"<key>SuccessfulExit</key><false/>", "<key>ThrottleInterval</key><integer>30</integer>", "<key>StandardOutPath</key><string>/dev/null</string>", "<key>StandardErrorPath</key><string>/dev/null</string>"} {
		if !strings.Contains(plan.Plist, fragment) {
			t.Fatalf("missing service lifecycle setting: %s", fragment)
		}
	}
}

func TestServiceOfflineInstallIdempotenceAndUninstall(t *testing.T) {
	plan := serviceFixture(t)
	parent := filepath.Dir(plan.PlistPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(parent, "another-app.plist")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	plan.runner = func(ctx context.Context, args ...string) error {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			t.Fatal("unbounded launchctl invocation")
		}
		calls = append(calls, append([]string(nil), args...))
		if _, err := os.Stat(plan.PlistPath); err != nil {
			t.Fatal("plist absent before bootstrap/bootout completed")
		}
		if _, err := os.Stat(plan.ReceiptPath); err != nil {
			t.Fatal("ownership receipt absent before launchctl")
		}
		return nil
	}
	if err := InstallService(plan); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"bootstrap", plan.Domain, plan.PlistPath}) {
		t.Fatalf("wrong bootstrap argv: %v", calls)
	}
	for _, path := range []string{plan.PlistPath, plan.ReceiptPath} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("service metadata not private")
		}
	}
	if info, err := os.Stat(parent); err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("changed pre-existing LaunchAgents directory mode")
	}
	if err := InstallService(plan); err != nil || len(calls) != 2 || calls[1][0] != "print" {
		t.Fatalf("identical setup restarted monitor: %v %v", calls, err)
	}
	if err := UninstallService(plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls[2], []string{"bootout", plan.Domain + "/" + plan.Label}) {
		t.Fatalf("wrong bootout argv: %v", calls[2])
	}
	for _, path := range []string{plan.PlistPath, plan.ReceiptPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("owned service file retained after successful uninstall")
		}
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "keep" {
		t.Fatal("uninstall touched unrelated app")
	}
}

func TestServiceRefusesForeignOrEditedPlist(t *testing.T) {
	for _, mode := range []string{"foreign", "edited", "symlink", "changed-plan"} {
		t.Run(mode, func(t *testing.T) {
			plan := serviceFixture(t)
			plan.runner = func(context.Context, ...string) error { return nil }
			if mode != "foreign" {
				if err := InstallService(plan); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(plan.PlistPath), 0700); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "foreign", "edited":
				if err := os.WriteFile(plan.PlistPath, []byte("not owned bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(plan.PlistPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(plan.ReceiptPath, plan.PlistPath); err != nil {
					t.Fatal(err)
				}
			case "changed-plan":
				plan.PlistPath = filepath.Join(plan.options.Home, "arbitrary-file")
			}
			plan.runner = func(context.Context, ...string) error { t.Fatal("unsafe launchctl invocation"); return nil }
			if err := InstallService(plan); err == nil {
				t.Fatal("installed over unverified file")
			}
			if err := UninstallService(plan); err == nil {
				t.Fatal("uninstalled unverified file")
			}
		})
	}
}

func TestServiceLaunchctlFailuresPreserveOwnership(t *testing.T) {
	plan := serviceFixture(t)
	plan.runner = func(context.Context, ...string) error { return errors.New("synthetic bootstrap failure") }
	if err := InstallService(plan); err == nil {
		t.Fatal("ignored bootstrap failure")
	}
	for _, path := range []string{plan.PlistPath, plan.ReceiptPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("failed new install left autostart/receipt behind")
		}
	}
	plan.runner = func(context.Context, ...string) error { return nil }
	if err := InstallService(plan); err != nil {
		t.Fatal(err)
	}
	plan.runner = func(context.Context, ...string) error { return errors.New("synthetic bootout failure") }
	if err := UninstallService(plan); err == nil {
		t.Fatal("ignored stop failure")
	}
	for _, path := range []string{plan.PlistPath, plan.ReceiptPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("deleted service metadata despite failed stop")
		}
	}
}
