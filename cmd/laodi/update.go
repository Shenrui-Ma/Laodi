package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const updateBootstrapURL = "https://raw.githubusercontent.com/Shenrui-Ma/Laodi-skills/main/install.sh"
const maxUpdateBootstrap = 1 << 20

var updateTagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

type updateCommands struct {
	platform   string
	executable func() (string, error)
	run        func(context.Context, string, ...string) error
}

func runUpdate(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runUpdateWith(ctx, args, updateCommands{
		platform: runtime.GOOS, executable: os.Executable,
		run: func(ctx context.Context, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			// A separate script file and EOF stdin prevent downloaded shell code
			// from consuming terminal input. Environment (including proxies) is inherited.
			return cmd.Run()
		},
	})
}

func runUpdateWith(ctx context.Context, args []string, commands updateCommands) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "下载并查看更新计划，不修改安装或通知权限")
	noNotifications := fs.Bool("no-notifications", false, "不请求系统通知权限")
	tag := fs.String("version", "", "指定官方发行标签；默认安装脚本推荐版本")
	stateDir := fs.String("state-dir", "", "状态目录；默认沿用当前安装位置")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (*format != "text" && *format != "json") {
		return errors.New("unexpected arguments or output format")
	}
	if *tag != "" && !validUpdateTag(*tag) {
		return errors.New("use a release tag such as v0.3.0-preview.2")
	}
	if commands.platform != "darwin" {
		return errors.New("release updates currently support macOS only")
	}
	if *stateDir == "" {
		executable, err := commands.executable()
		if err != nil {
			return fmt.Errorf("locate installed executable: %w", err)
		}
		*stateDir, err = installedUpdateStateDir(executable)
		if err != nil {
			return err
		}
	} else {
		absolute, err := filepath.Abs(*stateDir)
		if err != nil {
			return err
		}
		*stateDir = absolute
	}
	if strings.ContainsAny(*stateDir, "\x00\r\n") {
		return errors.New("invalid state directory")
	}

	dir, err := os.MkdirTemp("", "laodi-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "install.sh")
	file, err := os.OpenFile(script, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	downloadCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	err = commands.run(downloadCtx, "/usr/bin/curl", "-q", "--fail", "--silent", "--show-error", "--location",
		"--proto", "=https", "--proto-redir", "=https", "--connect-timeout", "15", "--max-time", "60",
		"--max-filesize", "1048576", "--output", script, updateBootstrapURL)
	cancel()
	if err != nil {
		return fmt.Errorf("download update installer (installed version unchanged): %w", err)
	}
	info, err := os.Lstat(script)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxUpdateBootstrap {
		return errors.New("invalid or oversized update installer; installed version unchanged")
	}
	forwarded := []string{script}
	if *tag != "" {
		// install.sh parses its version selector before installer arguments.
		forwarded = append(forwarded, "--version", *tag)
	}
	if *dryRun {
		forwarded = append(forwarded, "--dry-run")
	}
	if *noNotifications {
		forwarded = append(forwarded, "--no-notifications")
	}
	if *stateDir != "" {
		forwarded = append(forwarded, "--state-dir", *stateDir)
	}
	forwarded = append(forwarded, "--format", *format)
	if err := commands.run(ctx, "/bin/sh", forwarded...); err != nil {
		return fmt.Errorf("update installer: %w", err)
	}
	return nil
}

func validUpdateTag(tag string) bool {
	if len(tag) > 128 || !updateTagPattern.MatchString(tag) {
		return false
	}
	_, suffix, found := strings.Cut(tag, "-")
	if found {
		for _, part := range strings.Split(suffix, ".") {
			if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
				return false
			}
		}
	}
	return true
}

// Only an installed runtime with its receipt supplies a default custom state
// directory. A standalone release binary leaves the installer's normal default.
func installedUpdateStateDir(executable string) (string, error) {
	executable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve installed executable: %w", err)
	}
	dir := filepath.Dir(executable)
	if filepath.Base(dir) != "runtime" || filepath.Base(executable) != "laodi" {
		return "", nil
	}
	receiptPath := filepath.Join(dir, ".distribution.json")
	info, err := os.Lstat(receiptPath)
	if err != nil {
		return "", fmt.Errorf("read installation receipt; specify --state-dir if using a standalone binary: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxUpdateBootstrap {
		return "", errors.New("invalid installation receipt")
	}
	file, err := os.Open(receiptPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var receipt struct {
		Version int               `json:"version"`
		Files   map[string]string `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(file, maxUpdateBootstrap+1)).Decode(&receipt); err != nil || receipt.Version != 1 || receipt.Files["laodi"] == "" {
		return "", errors.New("invalid installation receipt")
	}
	return filepath.Dir(dir), nil
}
