package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
)

// ZCode's explicit native shell uses Node's non-cmd Windows contract: -c
// followed by a single opaque command. This is data, never shell source.
// All invalid inputs remain silent and fail open, like the ordinary hook entry.
func runPlatformHookShell(args []string, input io.Reader) bool {
	if len(args) == 0 || args[0] != "-c" {
		return false
	}
	if len(args) != 2 || len(args[1]) > 16384 || !strings.HasPrefix(args[1], "laodi-hook-v1:") {
		return true
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(args[1], "laodi-hook-v1:"))
	if err != nil {
		return true
	}
	var payload struct {
		Adapter  string `json:"adapter"`
		StateDir string `json:"state_dir"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF || payload.Adapter != "zcode" ||
		!filepath.IsAbs(payload.StateDir) || filepath.Clean(payload.StateDir) != payload.StateDir || strings.ContainsAny(payload.StateDir, "\x00\r\n") {
		return true
	}
	runHook([]string{"--adapter", payload.Adapter, "--state-dir", payload.StateDir}, input)
	return true
}
