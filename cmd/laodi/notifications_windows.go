package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

func platformNotifierProvider(stateDir, explicit string) func() string {
	if explicit != "" {
		return nil
	}
	executable, err := os.Executable()
	if err != nil || !strings.EqualFold(laodi.WindowsInstalledRoot(executable), stateDir) {
		return nil
	}
	return func() string { return laodi.ConfiguredWindowsNotifier(stateDir) }
}

func runPlatformNotifications(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "notifications", "--notification-activate", "--notification-open", "--send", "--register", "--unregister", "--status", "--test-template", "--test-activation":
	default:
		return false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return true, err
	}
	root := laodi.WindowsInstalledRoot(executable)
	if args[0] == "notifications" {
		if len(args) < 2 {
			return true, errors.New("notifications requires status, enable, disable, test-template, test-activation or test")
		}
		fs := flag.NewFlagSet("notifications", flag.ContinueOnError)
		state := fs.String("state-dir", root, "installed Windows state directory")
		if err := fs.Parse(args[2:]); err != nil {
			return true, err
		}
		if fs.NArg() != 0 {
			return true, errors.New("unexpected notification arguments")
		}
		if *state == "" {
			*state, err = laodi.DefaultStateDir("")
			if err != nil {
				return true, err
			}
		}
		data, err := laodi.ManageWindowsNotifications(context.Background(), *state, args[1])
		if len(data) > 0 {
			fmt.Fprintln(os.Stdout, string(data))
		}
		return true, err
	}
	action := args[0]
	switch action {
	case "--notification-activate":
		if len(args) > 2 || (len(args) == 2 && args[1] != "-Embedding") {
			return true, errors.New("invalid activation arguments")
		}
		args = []string{"--com-server"}
	case "--notification-open":
		if len(args) != 1 {
			return true, errors.New("invalid activation arguments")
		}
		args = []string{"--activate", "status"}
	case "--send", "--register", "--unregister", "--status", "--test-template", "--test-activation":
	default:
		return false, nil
	}
	if root == "" {
		return true, errors.New("notification relay requires a verified installed Windows version")
	}
	if action == "--register" || action == "--unregister" {
		data, err := laodi.ManageWindowsNotifications(context.Background(), root, strings.TrimPrefix(action, "--"))
		if len(data) > 0 {
			fmt.Fprintln(os.Stdout, string(data))
		}
		return true, err
	}
	if action == "--send" && laodi.ConfiguredWindowsNotifier(root) == "" {
		fmt.Fprintln(os.Stdout, `{"schema_version":1,"action":"send","ok":true,"delivery":"not_configured"}`)
		return true, nil
	}
	helper, err := laodi.WindowsNotificationTarget(root)
	if err != nil {
		return true, err
	}
	data, err := laodi.RunWindowsNotificationHelper(context.Background(), helper, root, args...)
	if len(data) > 0 {
		fmt.Fprintln(os.Stdout, string(data))
	}
	return true, err
}
