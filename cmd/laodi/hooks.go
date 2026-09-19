package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Shenrui-Ma/Laodi-skills/internal/laodi"
)

// The hook transport is intentionally silent and fail-open. It never emits a
// host decision, context injection, raw error or user content on either stream.
func runHook(args []string, input io.Reader) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	adapter := fs.String("adapter", "", "zcode or claude-code")
	stateDir := fs.String("state-dir", "", "private local state")
	if fs.Parse(args) != nil || fs.NArg() != 0 || (*adapter != "zcode" && *adapter != "claude-code") {
		return
	}
	// Installed hooks carry an explicit state path. Their host may sanitize
	// USERPROFILE/HOME, so resolve the interactive default only when needed.
	if *stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		*stateDir, err = laodi.DefaultStateDir(home)
		if err != nil {
			return
		}
	}
	if !filepath.IsAbs(*stateDir) {
		return
	}
	done := make(chan laodi.HookInspection, 1)
	go func() { done <- laodi.InspectHook(*adapter, input) }()
	select {
	case inspection := <-done:
		_ = laodi.SubmitHookInspection(*stateDir, inspection)
	case <-time.After(2 * time.Second):
		_ = laodi.SubmitHookInspection(*stateDir, laodi.HookInspection{Adapter: *adapter, Signals: []laodi.HookSignal{{Kind: "hook_coverage_degraded", Counts: map[string]int{"input_timeout": 1}}}})
	}
}

func checkHookInbox(stateDir string) laodi.AgentSummary {
	report := laodi.Report{SchemaVersion: laodi.SchemaVersion, Parser: laodi.HookParserID, Coverage: "hook_inbox_ready", CheckedAt: time.Now().UTC()}
	batch, err := laodi.ReadHookInbox(stateDir, 32)
	if err != nil {
		report.Coverage = "degraded"
		report.Diagnostics = []laodi.Diagnostic{{Code: "hook_inbox_unreadable"}}
	} else {
		report.Findings, report.Diagnostics = batch.Findings, batch.Diagnostics
		if batch.Pending > len(batch.Receipts) {
			report.Diagnostics = append(report.Diagnostics, laodi.Diagnostic{Code: "more_hook_records_pending"})
		}
	}
	summary := laodi.SummarizeReport(report)
	summary.Unknowns = append(summary.Unknowns, "hook_configuration_and_delivery_not_verified", "read_only_queue_sample_not_full_history")
	return summary
}

func runHooks(args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		fmt.Println("laodi hooks install|uninstall --adapter zcode|claude-code [--client-exe PATH] [--apply]\n默认预览；--apply只更改当前用户的对应Hook配置。新会话接入，已有任务不重启。\nlaodi hooks status [--state-dir PATH]\nlaodi hooks discover：只读检查标准安装位置、PATH及匹配进程的公开程序身份。\n持续处理事件需要运行watch或setup --apply；仅用工具检测可加--hooks-only。")
		return nil
	}
	if args[0] == "discover" {
		if len(args) != 1 {
			return fmt.Errorf("hooks discover takes no arguments")
		}
		return json.NewEncoder(os.Stdout).Encode(laodi.DiscoverClients())
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultState, err := laodi.DefaultStateDir(home)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("hooks", flag.ContinueOnError)
	adapter := fs.String("adapter", "", "zcode or claude-code")
	stateDir := fs.String("state-dir", defaultState, "private local state")
	apply := fs.Bool("apply", false, "apply configuration change")
	clientExe := fs.String("client-exe", "", "explicit Windows client executable with a verified hook contract")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || !filepath.IsAbs(*stateDir) {
		return fmt.Errorf("unexpected arguments or non-absolute state directory")
	}
	if args[0] == "status" {
		if *apply {
			return fmt.Errorf("status is read-only")
		}
		status, err := laodi.GetHookInboxStatus(*stateDir)
		if err != nil {
			return fmt.Errorf("hook inbox status unavailable")
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	if args[0] != "install" && args[0] != "uninstall" {
		return fmt.Errorf("unknown hooks command")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	plan, err := laodi.PlanHookConfig(laodi.HookConfigOptions{Adapter: *adapter, Executable: exe, StateDir: *stateDir, Home: home, ClientExecutable: *clientExe, Remove: args[0] == "uninstall"})
	if err != nil {
		return err
	}
	if !*apply {
		return json.NewEncoder(os.Stdout).Encode(plan)
	}
	if args[0] == "install" {
		err = laodi.InstallHookConfig(plan)
	} else {
		err = laodi.UninstallHookConfig(plan)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"action": args[0], "adapter": *adapter, "configuration_updated": true,
		"running_sessions_restarted": false, "runtime_delivery_verified": false,
		"next_session_required": true,
	})
}
