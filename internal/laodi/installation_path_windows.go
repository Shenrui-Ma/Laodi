//go:build windows

package laodi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var errWindowsInstallPathRedirected = errors.New("Windows installation path is redirected or virtualized")

// Package compatibility virtualization can apply even when the process reports
// no package identity. Task Scheduler and COM activation run outside that view,
// so a logical path that opens successfully here can fail for those processes.
// Diagnose the kernel handle path; never silently choose a different state root.
func verifyWindowsInstallationPath(dir string) error {
	if !localAbsoluteWindowsPath(dir) {
		return errors.New("Windows installation requires a clean absolute local path")
	}
	if err := windowsNoReparseAncestors(dir); err != nil {
		return err
	}
	// Planning must not create a directory. Check the closest existing ancestor
	// now, then repeat on the actual newly opened installation root before any
	// payload publication or background/notification registration.
	for candidate := dir; ; candidate = filepath.Dir(candidate) {
		file, err := os.Open(candidate)
		if errors.Is(err, os.ErrNotExist) && filepath.Dir(candidate) != candidate {
			continue
		}
		if err != nil {
			return err
		}
		err = verifyWindowsInstallationDirectoryHandle(candidate, file)
		file.Close()
		return err
	}
}

func verifyWindowsInstallationRoot(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	if err := verifyWindowsInstallationDirectoryHandle(root.Name(), file); err != nil {
		return err
	}
	return verifyWindowsInstallationStateEntries(root.Name())
}

// An existing real directory can contain separately virtualized files. This
// happens when an ordinary desktop install follows a packaged-process install:
// the directory handle is real, while the old current record and launchers still
// resolve to the package overlay. Inspect the public entry handles before any
// rooted record read, which otherwise fails with an unhelpful reparse error.
func verifyWindowsInstallationStatePath(dir string) error {
	if err := verifyWindowsInstallationPath(dir); err != nil {
		return err
	}
	return verifyWindowsInstallationStateEntries(dir)
}

func verifyWindowsInstallationStateEntries(dir string) error {
	for _, name := range []string{windowsCurrentName, "laodi.exe", "laodi-host.exe"} {
		err := verifyWindowsInstallationFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyWindowsInstallationDirectoryHandle(requested string, file *os.File) error {
	return verifyWindowsInstallationHandle(requested, file, true)
}

// External launchers must name the same public file outside the current process
// view, even when the selected state directory itself is not redirected.
func verifyWindowsInstallationFile(path string) error {
	if !localAbsoluteWindowsPath(path) {
		return errors.New("Windows installation requires a clean absolute local file path")
	}
	if err := windowsNoReparseAncestors(path); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return verifyWindowsInstallationHandle(path, file, false)
}

func verifyWindowsInstallationHandle(requested string, file *os.File, directory bool) error {
	if err := windowsCheckFileIdentity(syscall.Handle(file.Fd()), directory); err != nil {
		return err
	}
	actual, err := windowsFinalHandlePath(file)
	if err != nil {
		return fmt.Errorf("cannot verify Windows installation path: %w", err)
	}
	if !sameWindowsInstallationPath(requested, actual) {
		return fmt.Errorf("%w: requested %q opens as %q; run the installer from an ordinary desktop session or select an explicit --state-dir that has the same path outside the application sandbox; no installation root was changed", errWindowsInstallPathRedirected, requested, actual)
	}
	return nil
}

func sameWindowsInstallationPath(requested, actual string) bool {
	return localAbsoluteWindowsPath(requested) && localAbsoluteWindowsPath(actual) && strings.EqualFold(requested, actual)
}

func windowsFinalHandlePath(file *os.File) (string, error) {
	var path [32768]uint16
	n, _, err := fileKernel.NewProc("GetFinalPathNameByHandleW").Call(file.Fd(), uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)), 0)
	if n == 0 {
		return "", err
	}
	if n >= uintptr(len(path)) {
		return "", errors.New("final installation path exceeds the Windows path limit")
	}
	name := syscall.UTF16ToString(path[:n])
	if strings.HasPrefix(name, `\\?\`) {
		name = strings.TrimPrefix(name, `\\?\`)
	}
	if !localAbsoluteWindowsPath(name) {
		return "", errors.New("final installation path is not a clean local drive path")
	}
	return name, nil
}
