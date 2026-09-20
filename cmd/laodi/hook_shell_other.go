//go:build !windows

package main

import "io"

func runPlatformHookShell([]string, io.Reader) bool { return false }
