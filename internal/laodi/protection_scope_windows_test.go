package laodi

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsArchiveEnvironmentUsesProducerRootPrecedence(t *testing.T) {
	const home = `C:\Users\Synthetic`
	const other = `C:\Users\Other`
	for _, item := range []struct {
		name    string
		env     map[string]string
		allowed bool
	}{
		{"profile", map[string]string{"USERPROFILE": home}, true},
		{"home-wins", map[string]string{"HOME": home, "USERPROFILE": other}, true},
		{"base-wins", map[string]string{"ZCODE_DATA_BASE_DIR": home, "HOME": other, "USERPROFILE": other}, true},
		{"home-mismatch", map[string]string{"HOME": other, "USERPROFILE": home}, false},
		{"base-mismatch", map[string]string{"ZCODE_DATA_BASE_DIR": other, "HOME": home}, false},
		{"profile-mismatch", map[string]string{"USERPROFILE": other}, false},
		{"whitespace-falls-through", map[string]string{"ZCODE_DATA_BASE_DIR": " \t", "HOME": "  " + home + " \t"}, true},
		{"case-and-trailing-separator", map[string]string{"HOME": `c:\users\synthetic\`}, true},
		{"data-root-is-not-base", map[string]string{"ZCODE_DATA_BASE_DIR": home + `\.zcode\v2`}, false},
		{"relative", map[string]string{"HOME": `..\Synthetic`}, false},
		{"desktop-mismatch", map[string]string{"HOME": home, "ZCODE_DESKTOP_HOME_DIR": other}, false},
		{"desktop-home", map[string]string{"HOME": home, "ZCODE_DESKTOP_HOME_DIR": " " + home + " "}, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			err := checkWindowsArchiveEnvironment(home, func(key string) string { return item.env[key] })
			if (err == nil) != item.allowed {
				t.Fatalf("allowed=%v, error=%v", item.allowed, err)
			}
			if err != nil && (strings.Contains(err.Error(), home) || strings.Contains(err.Error(), other)) {
				t.Fatal("scope error disclosed a path")
			}
		})
	}
}

func TestWindowsArchiveSelfTestRejectsDifferentRootBeforeProbe(t *testing.T) {
	t.Setenv("ZCODE_DATA_BASE_DIR", "")
	t.Setenv("HOME", `C:\Users\Other`)
	t.Setenv("ZCODE_DESKTOP_HOME_DIR", "")
	if _, err := PlanZCodeProtection("", `C:\Users\Synthetic`, ""); err == nil || err.Error() != "alternate client cache roots require a separate protection profile" {
		t.Fatalf("enable did not reject mismatching root first: %v", err)
	}
	result, err := TestArchiveProtection(`C:\Users\Synthetic`, `C:\Users\Synthetic\absent`, "")
	if err == nil || result.Status != "unsupported_client" || result.GuardHealthy || result.DeniedOperations != 0 {
		t.Fatalf("out-of-scope self-test accepted: %+v, %v", result, err)
	}
}

func TestWindowsArchiveSummaryRechecksRootDespiteVerifiedBundle(t *testing.T) {
	f := newWindowsGuardFixture(t, false)
	f.enable(t)
	t.Setenv("ZCODE_DATA_BASE_DIR", "")
	t.Setenv("ZCODE_DESKTOP_HOME_DIR", "")
	t.Setenv("HOME", f.home)
	verified := func(string) bool { return true }
	if result := readArchiveProtectionSummary(f.home, f.state, "synthetic-client", verified); result.Status != "enabled" {
		t.Fatalf("matching scope rejected: %+v", result)
	}
	t.Setenv("HOME", filepath.Join(f.home, "other-base"))
	if result := readArchiveProtectionSummary(f.home, f.state, "synthetic-client", verified); result.Status != "unsupported_client" {
		t.Fatalf("different root reported healthy: %+v", result)
	}
	t.Setenv("ZCODE_DATA_BASE_DIR", f.home)
	if result := readArchiveProtectionSummary(f.home, f.state, "synthetic-client", verified); result.Status != "enabled" {
		t.Fatalf("higher-priority matching base rejected: %+v", result)
	}
}
