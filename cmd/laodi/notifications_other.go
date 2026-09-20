//go:build !windows

package main

func runPlatformNotifications([]string) (bool, error)       { return false, nil }
func platformNotifierProvider(string, string) func() string { return nil }
