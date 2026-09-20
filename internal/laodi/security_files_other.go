//go:build !windows

package laodi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func createPrivateFile(root *os.Root, name string, flags int) (*os.File, error) {
	return root.OpenFile(name, flags|os.O_CREATE|os.O_EXCL, 0600)
}

func privateDirectory(root *os.Root, name string) error {
	err := root.Mkdir(name, 0700)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func replaceStateFile(root *os.Root, oldName, newName string) error {
	return root.Rename(oldName, newName)
}

func syncStateDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Parent paths can include system aliases such as macOS /var -> /private/var.
// The state directory itself and files within it must not be symbolic links.
func openStateRoot(dir string, create bool) (*os.Root, error) {
	if dir == "" {
		return nil, errors.New("state directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			return nil, fmt.Errorf("create state parent: %w", err)
		}
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("resolve state parent: %w", err)
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	if create {
		if err := os.Mkdir(abs, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create state directory: %w", err)
		}
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("inspect state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("state directory must be a directory, not a symbolic link")
	}
	if info.Mode().Perm() != 0700 {
		return nil, errors.New("state directory permissions must be 0700")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open state directory: %w", err)
	}
	opened, err := root.Stat(".")
	after, afterErr := os.Lstat(abs)
	if err != nil || afterErr != nil || !after.IsDir() || !os.SameFile(info, opened) || !os.SameFile(after, opened) {
		root.Close()
		return nil, errors.New("state directory changed while opening")
	}
	return root, nil
}

func checkRegularFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file, not a symbolic link or special file", name)
	}
	if info.Mode().Perm() != 0600 {
		return fmt.Errorf("%s permissions must be 0600", name)
	}
	return nil
}

func openStateFile(root *os.Root, name string, flags int, create bool) (*os.File, error) {
	if create {
		f, err := root.OpenFile(name, flags|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create %s: %w", name, err)
		}
	}
	if err := checkRegularFile(root, name); err != nil {
		return nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	f, err := openExistingStateFile(root, name, flags)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	after, afterErr := root.Lstat(name)
	if err != nil || afterErr != nil || !after.Mode().IsRegular() || opened.Mode().Perm() != 0600 || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		f.Close()
		return nil, fmt.Errorf("%s changed while opening", name)
	}
	return f, nil
}
