//go:build !windows

package laodi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func checkHookPath(home, path string) error {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("hook settings path must remain inside the selected home")
	}
	current := home
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return errors.New("hook settings paths must not contain symbolic links or special files")
		}
	}
	return nil
}

func ensureHookConfigParent(plan HookConfigPlan) error {
	if err := checkHookPath(plan.options.Home, plan.ConfigPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plan.ConfigPath), 0700); err != nil {
		return errors.New("cannot create hook settings directory")
	}
	return checkHookPath(plan.options.Home, plan.ConfigPath)
}

func readHookConfigFile(path string) ([]byte, os.FileMode, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, 0600, err
	}
	defer parent.Close()
	name := filepath.Base(path)
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, 0600, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 {
		return nil, 0, errors.New("hook settings must be a regular file without group or world write permission")
	}
	f, err := openExistingStateFile(parent, name, os.O_RDONLY)
	if err != nil {
		return nil, 0, errors.New("cannot safely open hook settings")
	}
	defer f.Close()
	opened, err := f.Stat()
	after, afterErr := parent.Lstat(name)
	if err != nil || afterErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(after, opened) {
		return nil, 0, errors.New("hook settings changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHookConfigBytes+1))
	if err != nil || len(data) > maxHookConfigBytes {
		return nil, 0, errors.New("hook settings are unreadable or exceed 2 MiB")
	}
	return data, before.Mode().Perm(), nil
}

func replaceHookConfigFile(path string, data, expected []byte, mode os.FileMode) error {
	if len(data) > maxHookConfigBytes {
		return errors.New("hook settings exceed 2 MiB limit")
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return errors.New("cannot open hook settings parent")
	}
	defer parent.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("cannot name hook settings temporary file")
	}
	name := ".laodi-hook-" + hex.EncodeToString(random[:]) + ".tmp"
	f, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return errors.New("cannot create hook settings temporary file")
	}
	defer parent.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return errors.New("cannot write hook settings")
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errors.New("cannot sync hook settings")
	}
	if err := f.Close(); err != nil {
		return errors.New("cannot close hook settings")
	}
	current, currentMode, err := readHookConfigFile(path)
	if expected == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("hook settings appeared during installation; refusing to replace them")
		}
		// Publishing a previously absent file must be atomic and no-clobber.
		if err := parent.Link(name, filepath.Base(path)); err != nil {
			return errors.New("cannot publish new hook settings")
		}
	} else {
		if err != nil || !bytes.Equal(expected, current) || currentMode != mode {
			return errors.New("hook settings changed during installation; retry after the other editor finishes")
		}
		if err := parent.Rename(name, filepath.Base(path)); err != nil {
			return errors.New("cannot atomically replace hook settings")
		}
	}
	dir, err := parent.Open(".")
	if err != nil {
		return errors.New("hook settings saved but directory cannot be opened for sync")
	}
	defer dir.Close()
	return dir.Sync()
}
