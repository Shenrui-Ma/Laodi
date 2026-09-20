package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

func runProtect(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println("laodi protect enable [--dry-run]\nlaodi protect status [--format agent-summary]\nlaodi protect disable [--dry-run]\n限制已核验客户端的额外快照归档；首次启用和撤销需正常退出客户端。无需 sudo，不自动中断任务。")
		return nil
	}
	action := args[0]
	if action != "enable" && action != "status" && action != "disable" {
		return errors.New("unknown protection command")
	}
	fs := flag.NewFlagSet("protect "+action, flag.ContinueOnError)
	app := fs.String("app", "/Applications/ZCode.app", "已核验客户端路径")
	state := fs.String("state-dir", "", "老底状态目录")
	dry := fs.Bool("dry-run", false, "仅检查，不改变目录权限")
	format := fs.String("format", "text", "text, json or agent-summary")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (*format != "text" && *format != "json" && *format != "agent-summary") {
		return errors.New("unexpected protection arguments")
	}
	if runtime.GOOS != "darwin" {
		return errors.New("archive protection currently supports macOS only")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if *state == "" {
		*state = filepath.Join(home, "Library/Application Support/Laodi-skills")
	}
	*state, err = filepath.Abs(*state)
	if err != nil {
		return err
	}
	*app, err = filepath.Abs(*app)
	if err != nil {
		return err
	}
	if action == "status" {
		return printArchiveProtection(laodi.ReadArchiveProtectionSummary(home, *state, *app), *format)
	}
	var plan laodi.ArchiveGuardPlan
	if action == "enable" {
		// The version string alone is insufficient: require the audited archive.
		if _, err := laodi.PlanZCodeProtection(*app, home, ""); err != nil {
			return err
		}
		plan, err = laodi.PlanArchiveGuard(home)
		if err != nil {
			return err
		}
	}
	if *dry {
		pids, err := laodi.RunningZCodeProcesses(*app)
		if err != nil {
			return err
		}
		if *format != "text" {
			return json.NewEncoder(os.Stdout).Encode(struct {
				Action             string `json:"action"`
				DryRun             bool   `json:"dry_run"`
				ClientRunning      bool   `json:"client_running"`
				ExistingWorkspaces int    `json:"existing_workspaces"`
				ExistingArtifacts  int    `json:"existing_artifacts"`
			}{action, true, len(pids) > 0, plan.ExistingWorkspaces, plan.ExistingArtifacts})
		}
		fmt.Printf("仅预览 %s：客户端仍在运行: %t；已有工作区目录: %d，归档条目: %d。未改变权限。\n", action, len(pids) > 0, plan.ExistingWorkspaces, plan.ExistingArtifacts)
		return nil
	}
	// Never stop an existing task to apply or revoke filesystem restrictions.
	pids, err := laodi.RunningZCodeProcesses(*app)
	if err != nil {
		return err
	}
	if len(pids) > 0 {
		return errors.New("客户端仍在运行。请在任务结束后正常退出，再执行此命令；老底不会自动关闭客户端")
	}
	var result laodi.ArchiveGuardResult
	if action == "enable" {
		result, err = laodi.EnableArchiveGuard(plan, *state)
	} else {
		// Revocation remains available after an application update/uninstall.
		result, err = laodi.DisableArchiveGuard(home, *state)
	}
	if err != nil {
		if result.RecoveryNeeded {
			return errors.New("归档限制未完整变更；恢复记录已保留。请保持客户端退出，执行 protect disable；不要手动清除 ACL")
		}
		return err
	}
	return printArchiveProtection(laodi.SummarizeArchiveGuard(result), *format)
}

func printArchiveProtection(s laodi.ProtectionSummary, format string) error {
	if format != "text" {
		return json.NewEncoder(os.Stdout).Encode(s)
	}
	labels := map[string]string{
		"disabled": "未启用", "enabled": "已启用，当前目录权限检查通过",
		"degraded": "目录限制不完整，不能确认有效", "recovery_needed": "需要撤销未完成的权限变更",
		"unsupported_client": "客户端已变化，现有目录限制仍保留，效果未验证", "unknown": "暂时无法确认",
	}
	label := labels[s.Status]
	if label == "" {
		label = labels["unknown"]
	}
	fmt.Printf("老底 · 额外快照限制\n状态: %s\n受限工作区目录: %d；归档条目: %d\n", label, s.ProtectedWorkspaces, s.ProtectedArtifacts)
	fmt.Println("限制已核验的归档路径；不阻断普通模型请求、工具读取或其他上传路径。权限检查通过不代表观察到了上传尝试。")
	return nil
}
