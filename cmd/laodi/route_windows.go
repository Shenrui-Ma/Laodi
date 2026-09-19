package main

import (
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/Shenrui-Ma/Laodi-skills/internal/laodi"
)

func routeInstalled(args []string) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		if len(args) > 0 && args[0] == "hook" {
			return true, nil
		}
		return true, err
	}
	return routeWindowsExecutable(exe, args)
}

func routeWindowsExecutable(exe string, args []string) (handled bool, err error) {
	hook := len(args) > 0 && args[0] == "hook"
	// The launcher is part of the observer's fail-open boundary. Invalid
	// routing must neither run an unverified fallback nor fail an Agent tool.
	defer func() {
		if hook && err != nil {
			handled, err = true, nil
		}
	}()
	target, err := laodi.WindowsRoute(exe)
	if err != nil {
		return true, err
	}
	if target == "" {
		return false, nil
	}
	cmd := exec.Command(target, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if hook {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	if len(args) > 0 && (args[0] == "watch" || args[0] == "hook") {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	}
	return true, cmd.Run()
}
