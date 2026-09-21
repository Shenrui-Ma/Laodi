package laodi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ArchiveProtectionTestResult describes a synthetic filesystem probe, not an
// observed client action or proof that an upload was prevented.
type ArchiveProtectionTestResult struct {
	Status              string `json:"status"`
	ClientVerified      bool   `json:"client_verified"`
	GuardHealthy        bool   `json:"guard_healthy"`
	UnprotectedCreate   bool   `json:"unprotected_create"`
	ArchiveCreateDenied bool   `json:"archive_create_denied"`
	MetadataReadWrite   bool   `json:"metadata_read_write"`
	CleanupComplete     bool   `json:"cleanup_complete"`
	DeniedOperations    int    `json:"denied_operations"`
}

// TestArchiveProtection briefly exercises only newly created synthetic paths.
// It neither changes ACLs nor starts, interrupts or inspects client sessions.
func testArchiveProtectionDarwin(home, stateDir, app string) (ArchiveProtectionTestResult, error) {
	return testArchiveProtection(home, stateDir, app, func() error {
		_, err := PlanZCodeProtection(app, home, "")
		return err
	})
}

func testArchiveProtection(home, stateDir, app string, verifyClient func() error) (result ArchiveProtectionTestResult, err error) {
	result.Status = "failed"
	// Never expose filesystem error paths through this diagnostic API.
	defer func() {
		if err != nil {
			err = errors.New("archive protection self-test did not pass; inspect protection status")
		}
	}()
	principal, err := archivePrincipal()
	if err != nil {
		result.Status = "unsupported"
		return result, err
	}
	if err = archivePath(home, false); err != nil {
		return result, err
	}
	dir, err := archiveStatePath(home, stateDir, false)
	if err != nil {
		return result, err
	}
	if _, err = os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		result.Status = "disabled"
		return result, err
	} else if err != nil {
		return result, err
	}
	release, err := AcquireLock(dir)
	if err != nil {
		return result, err
	}
	defer release()
	receipt, err := archiveLoad(dir, home, principal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Status = "disabled"
		}
		return result, err
	}
	healthy := func() bool {
		status, inspectErr := archiveInspect(home, receipt, nil)
		return inspectErr == nil && status.Enabled && status.Healthy && !status.RecoveryNeeded
	}
	if !healthy() {
		return result, errors.New("guard is not healthy")
	}
	if verifyClient == nil || verifyClient() != nil {
		result.Status = "unsupported_client"
		return result, errors.New("client is not verified")
	}
	cache := filepath.Join(home, ".zcode", "v2", "checkpoints")
	if err = archivePath(cache, false); err != nil {
		return result, err
	}
	controlRoot, err := os.OpenRoot(dir)
	if err != nil {
		return result, err
	}
	defer controlRoot.Close()
	cacheRoot, err := os.OpenRoot(cache)
	if err != nil {
		return result, err
	}
	defer cacheRoot.Close()
	var random [6]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, err
	}
	name := hex.EncodeToString(random[:])
	control, controlClean, err := archiveProbe(controlRoot, "selftest-"+name, false)
	result.UnprotectedCreate = control.created
	if err != nil {
		result.CleanupComplete = controlClean
		return result, err
	}
	probe, probeClean, err := archiveProbe(cacheRoot, name, true)
	result.ArchiveCreateDenied = probe.denied
	result.MetadataReadWrite = probe.metadata
	result.CleanupComplete = controlClean && probeClean
	if probe.denied {
		result.DeniedOperations = 1
	}
	if err != nil {
		return result, err
	}
	result.GuardHealthy = healthy()
	result.ClientVerified = verifyClient() == nil
	if !result.GuardHealthy || !result.ClientVerified || !result.UnprotectedCreate || !result.ArchiveCreateDenied || !result.MetadataReadWrite || !result.CleanupComplete {
		return result, errors.New("probe evidence is incomplete")
	}
	result.Status = "passed"
	return result, nil
}

type archiveProbeResult struct{ created, denied, metadata bool }

// All cleanup is non-recursive and identity checked: a concurrent writer's
// unexpected entries cause failure rather than being removed.
func archiveProbe(parent *os.Root, name string, protected bool) (result archiveProbeResult, clean bool, err error) {
	if err = parent.Mkdir(name, 0700); err != nil {
		return result, true, err
	}
	identity, err := parent.Lstat(name)
	if err != nil || !identity.IsDir() || identity.Mode()&os.ModeSymlink != 0 {
		return result, false, errors.New("cannot identify probe directory")
	}
	defer func() {
		current, statErr := parent.Lstat(name)
		clean = statErr == nil && os.SameFile(identity, current) && parent.Remove(name) == nil
		if !clean && err == nil {
			err = errors.New("probe cleanup incomplete")
		}
	}()
	directory, err := parent.OpenRoot(name)
	if err != nil {
		return result, false, err
	}
	defer directory.Close()
	opened, err := directory.Stat(".")
	if err != nil || !os.SameFile(identity, opened) {
		return result, false, errors.New("probe directory changed")
	}
	mkdirErr := directory.Mkdir("tmp", 0700)
	result.created = mkdirErr == nil
	result.denied = errors.Is(mkdirErr, os.ErrPermission) || errors.Is(mkdirErr, syscall.EACCES) || errors.Is(mkdirErr, syscall.EPERM)
	if result.created {
		// It was empty and exclusively created by this probe; do not recurse.
		tmpIdentity, statErr := directory.Lstat("tmp")
		if statErr != nil || !tmpIdentity.IsDir() || tmpIdentity.Mode()&os.ModeSymlink != 0 {
			return result, false, errors.New("probe child changed")
		}
		current, statErr := directory.Lstat("tmp")
		if statErr != nil || !os.SameFile(tmpIdentity, current) {
			return result, false, errors.New("probe child changed during cleanup")
		}
		if err = directory.Remove("tmp"); err != nil {
			return result, false, err
		}
	}
	if protected && !result.denied || !protected && !result.created {
		return result, false, errors.New("unexpected directory access result")
	}
	if !protected {
		return result, false, nil
	}
	file, err := directory.OpenFile("checkpoint.json", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return result, false, err
	}
	fileIdentity, statErr := file.Stat()
	defer func() {
		closeErr := file.Close()
		current, currentErr := directory.Lstat("checkpoint.json")
		if statErr != nil || currentErr != nil || !os.SameFile(fileIdentity, current) || directory.Remove("checkpoint.json") != nil || closeErr != nil {
			err = errors.New("probe metadata cleanup incomplete")
		}
	}()
	const content = "{\"laodi_selftest\":true}\n"
	if _, err = io.WriteString(file, content); err != nil {
		return result, false, err
	}
	buf := make([]byte, len(content))
	if _, err = file.ReadAt(buf, 0); err != nil || string(buf) != content {
		return result, false, errors.New("probe metadata verification failed")
	}
	result.metadata = true
	return result, false, nil
}
