package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Shenrui-Ma/Laodi-skills/internal/laodi"
)

func TestHookExplicitStateDoesNotRequireUserProfile(t *testing.T) {
	// Real client subprocesses can have a restricted environment. An installed
	// callback already identifies its private queue and needs no home lookup.
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOME", "")
	for _, adapter := range []string{"zcode", "claude-code"} {
		t.Run(adapter, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			input := `{"hook_event_name":"PreToolUse","session_id":"synthetic-session","tool_use_id":"synthetic-read","tool_name":"Read","tool_input":{"file_path":"/synthetic/.env"}}`
			runHook([]string{"--adapter", adapter, "--state-dir", state}, strings.NewReader(input))
			batch, err := laodi.ReadHookInbox(state, 32)
			if err != nil || len(batch.Findings) != 1 {
				t.Fatalf("explicit-state callback lost without a user profile: %v %+v", err, batch)
			}
		})
	}
}
