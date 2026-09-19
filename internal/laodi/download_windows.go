//go:build windows

package laodi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func officialWindowsDownload(ctx context.Context, address string, limit int64) ([]byte, error) {
	allowed := func(u *url.URL) bool {
		return u.Scheme == "https" && u.User == nil && u.Port() == "" && (u.Host == "github.com" || u.Host == "release-assets.githubusercontent.com" || u.Host == "objects.githubusercontent.com")
	}
	u, err := url.Parse(address)
	if err != nil || !allowed(u) {
		return nil, errors.New("download must use official HTTPS release hosts")
	}
	client := http.Client{Timeout: 75 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !allowed(req.URL) {
			return errors.New("untrusted download redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, errors.New("oversized release download")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("oversized release download")
	}
	return data, nil
}

// ExtractWindowsRelease accepts a deliberately flat package. Validation happens
// for every entry before any bytes are extracted; aliases cannot overwrite an
// earlier file. The private destination must not already contain anything.
func ExtractWindowsRelease(data []byte, destination string) error {
	if len(data) > maxDistributionTotal {
		return errors.New("ZIP exceeds compressed size limit")
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if len(r.File) < 5 || len(r.File) > 8 {
		return errors.New("unexpected ZIP entry count")
	}
	allowed := map[string]bool{"laodi.exe": true, "laodi-host.exe": true, "LaodiNotify.exe": true, "laodi-logo.png": true, "SKILL.md": true, "usage.md": true, windowsManifestName: true}
	seen := map[string]bool{}
	var total uint64
	for _, f := range r.File {
		key := strings.ToLower(f.Name)
		if !allowed[f.Name] || seen[key] || !f.Mode().IsRegular() || f.Mode()&os.ModeSymlink != 0 || f.ExternalAttrs&0x400 != 0 || f.Flags&1 != 0 {
			return errors.New("unsafe, duplicate, or unexpected ZIP entry")
		}
		seen[key] = true
		if f.UncompressedSize64 > maxDistributionFile {
			return errors.New("ZIP entry exceeds size limit")
		}
		total += f.UncompressedSize64
		if total > maxDistributionTotal {
			return errors.New("ZIP exceeds expanded size limit")
		}
	}
	if _, err = os.Lstat(destination); err == nil {
		return errors.New("extraction destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	root, err := openStateRoot(destination, true)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, entry := range r.File {
		source, err := entry.Open()
		if err != nil {
			return err
		}
		file, err := createPrivateFile(root, entry.Name, os.O_WRONLY)
		if err != nil {
			source.Close()
			return err
		}
		n, err := io.Copy(file, io.LimitReader(source, maxDistributionFile+1))
		source.Close()
		if err != nil || uint64(n) != entry.UncompressedSize64 {
			file.Close()
			return errors.New("ZIP entry length or checksum mismatch")
		}
		if err = file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err = file.Close(); err != nil {
			return err
		}
	}
	_, err = readWindowsPayload(destination)
	return err
}

// DownloadWindowsRelease returns a private stage and an exact-path cleanup.
// Same-origin SHA256SUMS establishes consistency, not publisher authentication.
func DownloadWindowsRelease(ctx context.Context, tag, stageParent string) (string, func(), error) {
	if !validWindowsReleaseVersion(tag) {
		return "", nil, errors.New("a strict release version is required")
	}
	root, err := openStateRoot(stageParent, true)
	if err != nil {
		return "", nil, err
	}
	// Retain this directory handle so cleanup stays rooted at the original
	// private parent even if an ancestor's spelling changes during download.
	unpin, err := windowsPinRoot(root)
	if err != nil {
		root.Close()
		return "", nil, err
	}
	release := func() { unpin(); root.Close() }
	asset := "Laodi-skills-" + tag + "-windows-amd64.zip"
	base := "https://github.com/Shenrui-Ma/Laodi-skills/releases/download/" + tag + "/"
	sums, err := officialWindowsDownload(ctx, base+"SHA256SUMS-windows", 64<<10)
	if err != nil {
		release()
		return "", nil, err
	}
	want := ""
	matches := 0
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			want = fields[0]
			matches++
		}
	}
	if matches != 1 || !hashName.MatchString(want) {
		release()
		return "", nil, errors.New("release checksum entry missing or ambiguous")
	}
	data, err := officialWindowsDownload(ctx, base+asset, maxDistributionTotal)
	if err != nil {
		release()
		return "", nil, err
	}
	if serviceHash(data) != want {
		release()
		return "", nil, errors.New("release checksum mismatch")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		release()
		return "", nil, err
	}
	child := "download-" + hex.EncodeToString(nonce[:])
	dir := filepath.Join(stageParent, child)
	cleanup := func() { _ = root.RemoveAll(child); release() }
	if err = ExtractWindowsRelease(data, dir); err != nil {
		cleanup()
		return "", nil, err
	}
	m, err := readWindowsPayload(dir)
	if err != nil || m.Version != tag {
		cleanup()
		return "", nil, errors.New("downloaded version differs from requested release")
	}
	return dir, cleanup, nil
}
