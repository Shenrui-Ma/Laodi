package laodi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// NotifyArchiveProtectionTest is separate from incident storage: an explicit
// synthetic probe must never become evidence of an application's upload.
func NotifyArchiveProtectionTest(ctx context.Context, helper string, result ArchiveProtectionTestResult) string {
	if result.Status != "passed" || !result.ClientVerified || !result.GuardHealthy ||
		!result.UnprotectedCreate || !result.ArchiveCreateDenied || !result.MetadataReadWrite ||
		!result.CleanupComplete || result.DeniedOperations < 1 {
		return "test_not_passed"
	}
	if helper == "" {
		return "not_configured"
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "test_identifier_unavailable"
	}
	return sendNotice(ctx, helper, Event{ID: "archive_test_" + hex.EncodeToString(id[:]), Kind: "archive_protection_test_passed"})
}
