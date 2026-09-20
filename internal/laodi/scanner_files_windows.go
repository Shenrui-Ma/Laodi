//go:build windows

package laodi

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Check even cached entries: adding a hard link does not change file identity,
// size or mtime, but it changes whether this path is an accepted evidence file.
func checkScannerPath(root *os.Root, rel string, info os.FileInfo) error {
	full, err := windowsPrivatePath(filepath.Join(root.Name(), rel))
	if err != nil {
		return err
	}
	if err := windowsNoReparseAncestors(full); err != nil {
		return err
	}
	f, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := checkScannerHandle(f, info.IsDir()); err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("evidence file identity changed")
	}
	return nil
}

func checkScannerHandle(file *os.File, directory bool) error {
	return windowsCheckFileIdentity(syscall.Handle(file.Fd()), directory)
}
