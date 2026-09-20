//go:build !windows

package laodi

import "os"

func checkScannerPath(root *os.Root, rel string, info os.FileInfo) error { return nil }
func checkScannerHandle(file *os.File, directory bool) error             { return nil }
