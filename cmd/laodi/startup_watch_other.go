//go:build !windows

package main

import "context"

func runPlatformStartupWatch(context.Context, string, []string) (bool, error) { return false, nil }
