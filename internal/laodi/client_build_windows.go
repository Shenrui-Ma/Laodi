package laodi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// These hashes identify reviewed public program bytes, not a supported upload
// schema. This release writes GitCheckpointMeta records instead of the older
// repo_snapshot_manifest/v2 + upload state protocol. See the contract review.
const windowsZCodeReviewedVersion = "3.14.0.7681"
const windowsZCodeReviewedEXE = "564db1fd7b7c9cb71deca8178f7ffd865ace870fb0240e02c93a3300499118be"
const windowsZCodeReviewedASAR = "8604b5f47b0f4bf9e900901d8c60a0dcf6406b89879da872640ae27026b628cb"
const windowsZCodeReviewedCLI = "8f5cfccf2a899b92e57bc2a5760b949c1a928f739652fffc9e6d07c24f11ba05"

var windowsZCodeReviewedBuild = windowsSnapshotBuildID(windowsZCodeReviewedVersion, windowsZCodeReviewedEXE, windowsZCodeReviewedASAR, windowsZCodeReviewedCLI)

func windowsSnapshotBuildID(version, executable, archive, runtime string) string {
	return "windows:zcode:" + version + ":sha256:" + digest(executable+"\x00"+archive+"\x00"+runtime)
}

func windowsSnapshotProgramPaths(app string) []string {
	return []string{app, filepath.Join(filepath.Dir(app), "resources", "app.asar"), filepath.Join(filepath.Dir(app), "resources", "glm", "zcode.cjs")}
}

// DetectBuild reads only the executable and its public application resources.
// Namespace and content hashes prevent a matching macOS display version, or a
// replaced JS runtime next to an unchanged Electron executable, granting support.
func DetectBuild(app string) string {
	if !strings.EqualFold(filepath.Base(app), "ZCode.exe") {
		return "unknown"
	}
	identity, ok := inspectClientExecutable("zcode", app)
	if !ok || identity.FileVersion == "" {
		return "unknown"
	}
	paths := windowsSnapshotProgramPaths(app)
	hashes := make([]string, len(paths))
	for i, name := range paths {
		var err error
		hashes[i], err = hashWindowsSnapshotProgram(name)
		if err != nil {
			return "unknown"
		}
	}
	if hashes[0] != identity.SHA256 {
		return "unknown"
	}
	return windowsSnapshotBuildID(identity.FileVersion, hashes[0], hashes[1], hashes[2])
}

func hashWindowsSnapshotProgram(name string) (string, error) {
	if _, err := windowsPrivatePath(name); err != nil {
		return "", err
	}
	if err := windowsNoReparseAncestors(name); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		return "", err
	}
	defer root.Close()
	before, err := root.Lstat(filepath.Base(name))
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maxClientExecutableBytes {
		return "", errors.New("invalid or oversized public client program")
	}
	f, err := root.Open(filepath.Base(name))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := windowsCheckFileIdentity(syscall.Handle(f.Fd()), false); err != nil {
		return "", err
	}
	opened, err := f.Stat()
	if err != nil || !sameWindowsProgramFile(before, opened) {
		return "", errors.New("client program changed while opening")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, maxClientExecutableBytes+1))
	if err != nil || n != opened.Size() {
		return "", errors.New("client program changed while hashing")
	}
	after, err := root.Lstat(filepath.Base(name))
	if err != nil || !sameWindowsProgramFile(opened, after) {
		return "", errors.New("client program changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sameWindowsProgramFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func refreshClientBuild(s *Scanner) {
	if s.App == "" {
		return // Explicit --build remains available for synthetic fixtures.
	}
	paths := windowsSnapshotProgramPaths(s.App)
	current := make([]os.FileInfo, len(paths))
	changed := len(s.appFiles) != len(paths)
	for i, name := range paths {
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			s.Build, s.appFiles = "unknown", nil
			return
		}
		current[i] = info
		if !changed && !sameWindowsProgramFile(s.appFiles[i], info) {
			changed = true
		}
	}
	if changed {
		s.Build = DetectBuild(s.App)
		s.appFiles = current
	}
}

func supportedSnapshotBuild(build, app string) bool {
	// No reviewed Windows producer emits the upload schema parsed by Scanner.
	// Keep the explicit synthetic override, but never apply macOS's build gate
	// to a real Windows app or to a namespaced Windows program identity.
	return app == "" && build == KnownBuild
}

func unsupportedSnapshotBuildDiagnostic(build string) string {
	if build == windowsZCodeReviewedBuild {
		return "windows_zcode_git_checkpoint_schema_unsupported"
	}
	return "zcode_build_not_verified"
}
