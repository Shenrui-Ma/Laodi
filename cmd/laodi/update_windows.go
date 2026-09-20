package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"time"

	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

func runWindowsUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	tag := fs.String("version", "", "official Windows release tag (required until the first verified Windows release)")
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
	if fs.NArg() != 0 || !validUpdateTag(*tag) || (*format != "text" && *format != "json") {
		return errors.New("specify --version with a verified Windows release tag")
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	source, cleanup, err := laodi.DownloadWindowsRelease(ctx, *tag, filepath.Join(*state, "downloads"))
	if err != nil {
		return err
	}
	defer cleanup()
	forward := []string{"--source-dir", source, "--state-dir", *state, "--format", *format}
	if *dry {
		forward = append(forward, "--dry-run")
	}
	if *noNotify {
		forward = append(forward, "--no-notifications")
	}
	return runDistribution("install", forward)
}
