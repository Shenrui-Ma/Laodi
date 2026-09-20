package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

func runDistribution(action string, args []string) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "只查看安装/移除计划，不修改系统或请求通知权限")
	noNotifications := fs.Bool("no-notifications", false, "安装时不请求系统通知权限")
	source := fs.String("source-dir", "", "发行包目录；默认当前可执行文件所在目录")
	stateDir := fs.String("state-dir", "", "本工具状态目录；默认当前用户私有应用数据目录")
	clientExe := fs.String("client-exe", "", "Windows 客户端可执行文件；默认发现已验证版本")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (*format != "text" && *format != "json") {
		return fmt.Errorf("unexpected arguments or output format")
	}
	if *source != "" {
		absolute, err := filepath.Abs(*source)
		if err != nil {
			return err
		}
		*source = absolute
	}
	plan, err := laodi.PlanDistribution(laodi.DistributionOptions{
		SourceDir: *source, StateDir: *stateDir,
		ClientExecutable:     *clientExe,
		Remove:               action == "remove",
		RequestNotifications: action == "install" && !*noNotifications,
	})
	if err != nil {
		return err
	}
	if *dryRun {
		if *format == "json" {
			return json.NewEncoder(os.Stdout).Encode(plan)
		}
		fmt.Printf("老底 · %s预览\n固定安装位置: %s\n已发现适配工具: %d\n将更新现有程序: %t\n有待恢复的更新: %t\n本次不修改配置、不注册服务、不请求通知权限。\n", action, plan.RuntimeDir, len(plan.Adapters), plan.Upgrade, plan.PendingRecovery)
		return nil
	}
	var result laodi.DistributionResult
	if action == "install" {
		if *format == "text" && plan.RequestNotifications && len(plan.Adapters) > 0 {
			if runtime.GOOS == "windows" {
				fmt.Println("正在安装老底并注册通知身份。Windows 设置可能关闭通知；是否可见需另行确认。")
			} else {
				fmt.Println("正在安装老底。如出现系统通知授权，请选择是否允许提醒。")
			}
		}
		result, err = laodi.InstallDistribution(plan)
	} else {
		result, err = laodi.UninstallDistribution(plan)
	}
	if *format == "json" {
		if outputErr := json.NewEncoder(os.Stdout).Encode(result); outputErr != nil {
			return outputErr
		}
	} else {
		if action == "install" {
			if result.Updated {
				fmt.Println("老底程序已更新。")
			}
			fmt.Printf("老底 · 安装结果\n程序已复制到固定位置: %t\n已接入工具: %d\n后台服务已注册: %t\n通知状态: %s\n", result.RuntimeInstalled, len(result.HookAdapters), result.ServiceInstalled, distributionNotificationLabel(result.NotificationStatus))
			if result.ServiceInstalled {
				fmt.Println("当前Agent任务保持运行；工具事件接入请在下一次新会话验证。")
			}
			fmt.Printf("程序位置: %s\n可用命令: update、status、incidents、remove。\n", plan.Executable)
		} else {
			fmt.Println("老底 · 移除结果：仅处理自有接入，程序与历史记录保留。")
		}
		for _, warning := range result.Warnings {
			fmt.Printf("提示: %s\n", warning)
		}
	}
	return err
}

func distributionNotificationLabel(status string) string {
	switch status {
	case "authorized":
		return "已开启"
	case "provisional":
		return "临时授权"
	case "denied":
		return "已关闭"
	case "not_determined":
		return "尚未授权"
	case "not_requested":
		return "本次未请求授权"
	case "unknown":
		return "暂未确认"
	case "enabled":
		return "系统设置允许通知，实际送达待确认"
	case "disabled":
		return "Windows 设置已关闭通知"
	case "registered_status_unknown":
		return "通知身份已注册，系统设置与送达待确认"
	case "registration_failed":
		return "通知身份注册失败"
	default:
		return status
	}
}
