package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

type windowsUpdateCommands struct {
	recommend func(context.Context, string) (string, error)
	download  func(context.Context, string, string) (string, func(), error)
	run       func(context.Context, string, ...string) error
}

func runWindowsUpdate(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return runWindowsUpdateWith(ctx, args, windowsUpdateCommands{
		recommend: laodi.RecommendedWindowsRelease,
		download:  laodi.DownloadWindowsRelease,
		run:       runWindowsUpdateInstaller,
	})
}

func runWindowsUpdateInstaller(ctx context.Context, executable string, args ...string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// Nil stdin supplies EOF. The verified installer receives an argument array,
	// never a shell command, and must finish before its download is removed.
	return cmd.Run()
}

func runWindowsUpdateWith(ctx context.Context, args []string, commands windowsUpdateCommands) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	tag := fs.String("version", "", "official release tag; defaults to the Windows recommended channel")
	state := fs.String("state-dir", "", "existing Windows installation")
	dry := fs.Bool("dry-run", false, "download and inspect without switching versions")
	noNotify := fs.Bool("no-notifications", false, "do not register notification integration")
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
	explicitVersion := false
	fs.Visit(func(f *flag.Flag) { explicitVersion = explicitVersion || f.Name == "version" })
	if explicitVersion && !validUpdateTag(*tag) {
		return errors.New("specify --version with a valid release tag")
	}
	if !explicitVersion {
		recommended, err := commands.recommend(ctx, version)
		if err != nil {
			return fmt.Errorf("Windows recommended channel unavailable (installation unchanged; retry or use --version): %w", err)
		}
		*tag = recommended
	}
	if *state == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		dir := filepath.Dir(exe)
		if filepath.Base(filepath.Dir(dir)) == "versions" {
			*state = filepath.Dir(filepath.Dir(dir))
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			*state, err = laodi.DefaultStateDir(home)
			if err != nil {
				return err
			}
		}
	}
	source, cleanup, err := commands.download(ctx, *tag, filepath.Join(*state, "downloads"))
	if err != nil {
		return err
	}
	defer cleanup()
	forward := []string{"install", "--source-dir", source, "--state-dir", *state, "--format", *format}
	if *dry {
		forward = append(forward, "--dry-run")
	}
	if *noNotify {
		forward = append(forward, "--no-notifications")
	}
	// Use the downloaded version's installation logic, including fixes that the
	// currently running version cannot know about.
	if err := commands.run(ctx, filepath.Join(source, "laodi.exe"), forward...); err != nil {
		return fmt.Errorf("update installer: %w", err)
	}
	return nil
}
