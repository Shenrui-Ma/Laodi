//go:build !windows

package main

import "errors"

func routeInstalled([]string) (bool, error) { return false, nil }
func runWindowsUpdate([]string) error       { return errors.New("Windows only") }
