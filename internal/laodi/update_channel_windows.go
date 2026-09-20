//go:build windows

package laodi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const windowsRecommendedChannelURL = "https://raw.githubusercontent.com/Shenrui-Ma/Laodi/main/channels/windows-amd64.json"
const maxWindowsChannelBytes = 4096

// RecommendedWindowsRelease resolves only the maintained Windows channel. It
// cannot supply download URLs or bypass release checksum/payload validation.
// Trust is the same repository over HTTPS, not a separately signed manifest.
func RecommendedWindowsRelease(ctx context.Context, current string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		// The repository source path is fixed; redirects are not a channel
		// migration mechanism. A repository move requires a reviewed update.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Windows channel redirects are not allowed")
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, windowsRecommendedChannelURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Windows channel returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxWindowsChannelBytes {
		return "", errors.New("oversized Windows channel")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWindowsChannelBytes+1))
	if err != nil {
		return "", err
	}
	tag, err := parseWindowsRecommendedChannel(data)
	if err != nil {
		return "", err
	}
	// A candidate can be newer than the public recommendation. Only an
	// explicit --version may intentionally select an older release. Development
	// builds without a strict release tag have no comparable release identity.
	if validWindowsReleaseVersion(current) && compareWindowsReleaseVersions(tag, current) < 0 {
		return "", fmt.Errorf("recommended release %s is older than running release %s; use --version only for an intentional rollback", tag, current)
	}
	return tag, nil
}

func parseWindowsRecommendedChannel(data []byte) (string, error) {
	invalid := errors.New("invalid Windows recommended channel metadata")
	if len(data) > maxWindowsChannelBytes || !utf8.Valid(data) {
		return "", invalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return "", invalid
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return "", invalid
		}
		name, ok := key.(string)
		if !ok || fields[name] != nil {
			return "", invalid
		}
		switch name {
		case "schema", "channel", "platform", "version":
		default:
			return "", invalid
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return "", invalid
		}
		fields[name] = value
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') || len(fields) != 4 {
		return "", invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return "", invalid
	}
	var schema int
	var channel, platform, tag string
	if json.Unmarshal(fields["schema"], &schema) != nil || schema != 1 ||
		json.Unmarshal(fields["channel"], &channel) != nil || channel != "recommended" ||
		json.Unmarshal(fields["platform"], &platform) != nil || platform != "windows/amd64" ||
		json.Unmarshal(fields["version"], &tag) != nil || !validWindowsReleaseVersion(tag) {
		return "", invalid
	}
	_, suffix, _ := strings.Cut(tag, "-")
	if suffix != "windows" && !strings.HasPrefix(suffix, "windows.") {
		return "", invalid
	}
	return tag, nil
}

// Inputs are already strict SemVer tags. Compare decimal identifiers as strings
// to avoid integer overflow for valid, arbitrarily large version components.
func compareWindowsReleaseVersions(a, b string) int {
	compareNumber := func(a, b string) int {
		if len(a) < len(b) {
			return -1
		}
		if len(a) > len(b) {
			return 1
		}
		return strings.Compare(a, b)
	}
	ac, ap, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	bc, bp, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	av, bv := strings.Split(ac, "."), strings.Split(bc, ".")
	for i := range av {
		if c := compareNumber(av[i], bv[i]); c != 0 {
			return c
		}
	}
	if ap == "" && bp != "" {
		return 1
	}
	if ap != "" && bp == "" {
		return -1
	}
	av, bv = strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(av) && i < len(bv); i++ {
		an, bn := strings.Trim(av[i], "0123456789") == "", strings.Trim(bv[i], "0123456789") == ""
		var c int
		switch {
		case an && bn:
			c = compareNumber(av[i], bv[i])
		case an:
			c = -1
		case bn:
			c = 1
		default:
			c = strings.Compare(av[i], bv[i])
		}
		if c != 0 {
			return c
		}
	}
	if len(av) < len(bv) {
		return -1
	}
	if len(av) > len(bv) {
		return 1
	}
	return 0
}
