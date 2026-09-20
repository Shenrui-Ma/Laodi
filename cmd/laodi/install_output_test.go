package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

func distributionOutput(t *testing.T, result laodi.DistributionResult, failure error) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	previous := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = previous }()
	printDistributionResult("install", laodi.DistributionPlan{Executable: "synthetic-laodi"}, result, failure)
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestStagedRuntimeDoesNotClaimSuccessfulBackgroundInstallation(t *testing.T) {
	output := distributionOutput(t, laodi.DistributionResult{RuntimeInstalled: true, NotificationStatus: "not_configured"}, errors.New("synthetic service failure"))
	for _, want := range []string{"安装未完成", "后台监控未确认运行", "工具接入尚未完成"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing actionable partial-install status %q: %s", want, output)
		}
	}
	if strings.Contains(output, "工具事件接入请在下一次新会话验证") {
		t.Fatal("failed background install claimed readiness")
	}
}

func TestRunningMonitorWithoutAdaptersReportsNoToolCoverage(t *testing.T) {
	output := distributionOutput(t, laodi.DistributionResult{RuntimeInstalled: true, ServiceInstalled: true}, nil)
	if !strings.Contains(output, "尚未接入编程工具") {
		t.Fatal("zero-adapter installation hid its observation gap")
	}
}

func TestLoginStartupModeIsExplainedWithoutLeakingDiagnostics(t *testing.T) {
	output := distributionOutput(t, laodi.DistributionResult{RuntimeInstalled: true, ServiceInstalled: true,
		HookAdapters: []string{"synthetic"}, BackgroundMode: "user_startup", BackgroundReason: "task_access_denied"}, nil)
	if !strings.Contains(output, "当前用户登录启动") || !strings.Contains(output, "系统拒绝计划任务") {
		t.Fatal("login startup fallback was not explained")
	}
	if strings.Contains(output, "CLIXML") {
		t.Fatal("raw native diagnostics reached the user-facing report")
	}
}
