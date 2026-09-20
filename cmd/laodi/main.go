package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

var version = "0.4.1-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "laodi:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) > 0 && args[0] == "protect" {
		return runProtect(args[1:])
	}
	if len(args) > 0 && args[0] == "update" {
		return runUpdate(args[1:])
	}
	if len(args) > 0 && (args[0] == "install" || args[0] == "remove") {
		return runDistribution(args[0], args[1:])
	}
	if len(args) > 0 && args[0] == "hook" {
		runHook(args[1:], os.Stdin)
		return nil
	}
	if len(args) > 0 && args[0] == "hooks" {
		return runHooks(args[1:])
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println("Laodi — 本地隐私监测与可选的 Git 历史打包限制（BETA）。\n\n发行包: install [--dry-run] | update [--dry-run] [--version TAG] | remove [--dry-run]\n命令: check | watch | status | incidents | doctor | setup | uninstall | hooks | protect | version\n保护: protect enable [--dry-run] | protect status | protect disable\n选项: --root PATH --state-dir PATH --app PATH --build BUILD --format text|json|agent-summary\nwatch: --interval 2s --duration 30s --notifier /path/to/helper [--hooks-only]\n工具适配: hooks install --adapter zcode|claude-code [--apply]；hooks status查看队列\nsetup/uninstall: 默认只预览，--apply 才注册/移除用户级服务（macOS，无需sudo）\n\n首次扫描只建立既有记录基线。查询不请求通知权限，不改变客户端设置。watch前台退出用 Ctrl-C。")
		return nil
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Println(version)
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := fs.String("root", filepath.Join(home, ".zcode", "v2", "checkpoints"), "ZCode证据目录；不会跟随manifest中的路径读取源码")
	data := fs.String("state-dir", filepath.Join(home, "Library", "Application Support", "Laodi-skills"), "仅本工具状态目录")
	app := fs.String("app", "/Applications/ZCode.app", "只读版本发现")
	build := fs.String("build", "", "仅合成/已核对构建测试使用；默认读取应用CFBundleVersion")
	format := fs.String("format", "text", "text, json, agent-summary")
	interval := fs.Duration("interval", 2*time.Second, "有界元数据检查间隔")
	duration := fs.Duration("duration", 0, "0为前台持续运行")
	notifier := fs.String("notifier", "", "显式通知helper；留空只记录，不请求权限")
	apply := fs.Bool("apply", false, "显式安装/移除用户级后台服务")
	hooksOnly := fs.Bool("hooks-only", false, "仅接收工具事件，不要求安装ZCode或读取快照")
	if err = fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *apply && args[0] != "setup" && args[0] != "uninstall" {
		return fmt.Errorf("--apply is only valid with setup or uninstall")
	}
	if *format != "text" && *format != "json" && *format != "agent-summary" {
		return fmt.Errorf("unknown format")
	}
	*root, err = filepath.Abs(*root)
	if err != nil {
		return err
	}
	*data, err = filepath.Abs(*data)
	if err != nil {
		return err
	}
	buildOverride := *build
	discoverBuild := *build == "" && !*hooksOnly
	if discoverBuild {
		*build = laodi.DetectBuild(*app)
	}
	scanner := &laodi.Scanner{Root: *root, Build: *build}
	showSummary := func(s laodi.AgentSummary) error {
		laodi.AddArchiveProtectionSummary(&s, home, *data, *app)
		return printSummary(s, *format)
	}
	if discoverBuild {
		scanner.App = *app
	}
	switch args[0] {
	case "check":
		if *hooksOnly {
			return showSummary(checkHookInbox(*data))
		}
		r := scanner.Scan()
		return showSummary(laodi.SummarizeReport(r))
	case "watch":
		if *notifier != "" && !filepath.IsAbs(*notifier) {
			return fmt.Errorf("notifier must be an explicit absolute path")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return laodi.Watch(ctx, scanner, laodi.WatchOptions{StateDir: *data, Interval: *interval, Duration: *duration, Notifier: *notifier, Output: os.Stdout, HooksOnly: *hooksOnly, ProtectionHome: home})
	case "status", "incidents":
		st, e := laodi.LoadState(*data)
		if e != nil {
			return e
		}
		if !st.Initialized {
			return showSummary(laodi.AgentSummary{SchemaVersion: 1, Coverage: "not_initialized", Parser: laodi.ParserID, Counts: map[string]int{}, Unknowns: []string{"monitor_not_started"}})
		}
		s := laodi.SummarizeState(st)
		if !st.Running || time.Since(st.LastCheckedAt) > 75*time.Second {
			s.Coverage = "monitor_stale_or_stopped"
		}
		return showSummary(s)
	case "doctor":
		if *hooksOnly {
			s := checkHookInbox(*data)
			s.Unknowns = append(s.Unknowns, "system_notification_permission_not_checked", "background_service_not_installed_by_this_command")
			return showSummary(s)
		}
		r := scanner.Scan()
		s := laodi.SummarizeReport(r)
		s.Unknowns = append(s.Unknowns, "system_notification_permission_not_checked", "background_service_not_installed_by_this_command")
		return showSummary(s)
	case "setup", "uninstall":
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		plan, e := laodi.PlanService(laodi.ServiceOptions{Executable: exe, Root: *root, StateDir: *data, App: *app, Build: buildOverride, Notifier: *notifier, Home: home, HooksOnly: *hooksOnly})
		if e != nil {
			return e
		}
		if !*apply {
			if *format != "text" {
				return json.NewEncoder(os.Stdout).Encode(plan)
			}
			fmt.Printf("预览 %s：用户级任务 %s\n配置：%s\n命令：%q\n尚未更改任何服务。只有显式 --apply 才执行。\n", args[0], plan.Label, plan.PlistPath, plan.Arguments)
			return nil
		}
		if args[0] == "setup" {
			e = laodi.InstallService(plan)
		} else {
			e = laodi.UninstallService(plan)
		}
		if e != nil {
			return e
		}
		if args[0] == "setup" {
			if *format == "text" {
				fmt.Println("用户级服务已接入。以下是安装时已存在的线索，不代表刚刚发生；后台运行请用 status 检查，通知权限请用 LaodiNotify --status 检查。")
			}
			// Do not hide pre-existing evidence behind the watcher's quiet baseline.
			// This check is read-only and independent of the service's state writer.
			if *hooksOnly {
				return printSummary(laodi.AgentSummary{SchemaVersion: 1, Parser: laodi.HookParserID, Coverage: "service_registered", Counts: map[string]int{}, Unknowns: []string{"tool_hook_delivery_not_verified", "notification_delivery_not_verified"}}, *format)
			}
			return printSummary(laodi.SummarizeReport(scanner.Scan()), *format)
		}
		if *format != "text" {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"action": "uninstall", "service": "removed"})
		}
		fmt.Println("用户级服务已移除；已有事件记录保留。")
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func printSummary(s laodi.AgentSummary, format string) error {
	if format != "text" {
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(s)
	}
	fmt.Println("老底 · 本地隐私线索")
	fmt.Printf("范围状态: %s\n解析器: %s\n", s.Coverage, s.Parser)
	if s.Protection != nil {
		if err := printArchiveProtection(*s.Protection, "text"); err != nil {
			return err
		}
	}
	for _, source := range []string{"zcode", "claude-code"} {
		if n := s.SourceCounts[source]; n > 0 {
			fmt.Printf("%s 工具事件: %d\n", source, n)
		}
	}
	found := false
	for _, item := range [][2]string{
		{"sensitive_manifest_match", "含 Git 历史对象的快照清单"},
		{"upload_attempt_recorded", "含 Git 历史对象的上传尝试记录"},
		{"upload_acceptance_recorded", "含 Git 历史对象的客户端接受记录"},
		{"workspace_snapshot_manifest", "其他工作区快照清单"},
		{"workspace_snapshot_upload_attempt_recorded", "其他工作区快照上传尝试记录"},
		{"workspace_snapshot_upload_acceptance_recorded", "其他工作区快照客户端接受记录"},
		{"global_config_manifest_match", "附加配置快照清单"},
		{"global_config_upload_attempt_recorded", "附加配置上传尝试记录"},
		{"global_config_upload_acceptance_recorded", "附加配置客户端接受记录"},
		{"sensitive_tool_access_requested", "敏感位置工具访问请求（仅记录）"},
		{"sensitive_tool_output_detected", "工具输出中的疑似凭据"},
		{"hook_coverage_degraded", "工具事件检测缺口"},
		{"protection_coverage_degraded", "额外快照限制需要检查"},
		{"monitor_capacity_degraded", "快照记录容量不足（工具检测继续）"},
	} {
		if n := s.Counts[item[0]]; n > 0 {
			fmt.Printf("%s: %d\n", item[1], n)
			found = true
		}
	}
	if !found {
		fmt.Println("当前查询未发现已支持的风险线索；这不等于确认没有隐私外传。")
	}
	for _, d := range s.Diagnostics {
		fmt.Printf("检查未完整: %s\n", d.Code)
	}
	notifications := map[string]int{}
	for _, event := range s.Events {
		notifications[event.Notification]++
	}
	for _, item := range [][2]string{
		{"not_configured", "通知未接入"}, {"not_authorized", "通知未获授权"},
		{"helper_failed", "通知程序失败"}, {"not_available", "通知暂不可用"},
		{"invalid_helper_response", "通知响应异常"}, {"not_accepted", "通知未被接受"},
		{"unknown_timeout", "通知送达状态未知"}, {"unknown_after_restart", "重启前通知状态未知"},
		{"queued", "通知排队中"}, {"queue_full", "通知队列已满"}, {"dispatching", "通知提交中"},
		{"accepted_by_os", "系统已接受通知（不代表已看到）"}, {"aggregated", "重复提醒已合并"},
		{"recorded_only", "仅记录"}, {"suppressed_baseline", "已有记录静默保存"},
		{"suppressed_upgrade_baseline", "升级基线静默保存"},
	} {
		if n := notifications[item[0]]; n > 0 {
			fmt.Printf("%s: %d\n", item[1], n)
		}
	}
	if len(s.Events) > 0 {
		fmt.Printf("最近记录时间: %s\n", s.Events[len(s.Events)-1].ObservedAt.Local().Format(time.RFC3339))
	}
	fmt.Println("记录是客户端本地证据，不代表已独立确认上传内容或云端留存。不会阻断当前任务。")
	return nil
}
