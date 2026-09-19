package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Shenrui-Ma/Laodi-skills/internal/laodi"
)

func runDistribution(action string, args []string) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "只查看安装/移除计划，不修改系统或请求通知权限")
	noNotifications := fs.Bool("no-notifications", false, "安装时不请求系统通知权限")
	source := fs.String("source-dir", "", "发行包目录；默认当前可执行文件所在目录")
	stateDir := fs.String("state-dir", "", "本工具状态目录；默认当前用户Library/Application Support下")
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
		fmt.Printf("老底 · %s预览\n固定安装位置: %s\n已发现适配工具: %d\n本次不修改配置、不注册服务、不请求通知权限。\n", action, plan.RuntimeDir, len(plan.Adapters))
		return nil
	}
	var result laodi.DistributionResult
	if action == "install" {
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
			fmt.Printf("老底 · 安装结果\n程序已复制到固定位置: %t\n已接入工具: %d\n后台服务已注册: %t\n通知状态: %s\n", result.RuntimeInstalled, len(result.HookAdapters), result.ServiceInstalled, result.NotificationStatus)
			if result.ServiceInstalled {
				fmt.Println("当前Agent任务保持运行；工具事件接入请在下一次新会话验证。")
			}
			fmt.Printf("程序位置: %s\n可用命令: status、incidents、remove。\n", plan.Executable)
		} else {
			fmt.Println("老底 · 移除结果：仅处理自有接入，程序与历史记录保留。")
		}
		for _, warning := range result.Warnings {
			fmt.Printf("提示: %s\n", warning)
		}
	}
	return err
}
