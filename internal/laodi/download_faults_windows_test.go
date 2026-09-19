//go:build windows

package laodi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func windowsInstallationSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[rel] = "directory"
			return nil
		}
		if entry.Name() == ".lock" {
			// LockFileEx is mandatory on Windows: reading even an empty file's
			// locked byte range is correctly refused while another writer holds it.
			info, err := entry.Info()
			if err != nil {
				return err
			}
			snapshot[rel] = fmt.Sprintf("kernel-lock:%d", info.Size())
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[rel] = serviceHash(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func windowsSeedDurableHistoryAndQueue(t *testing.T, dir string) {
	t.Helper()
	state := emptyState()
	if _, err := ApplyReport(&state, storeReport(storeFinding("synthetic-history", "v1")), "synthetic-root-identity", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := SubmitHookInspection(dir, inboxInspection("synthetic-preserved-observation")); err != nil {
		t.Fatal(err)
	}
}

// The URL stays the official HTTPS URL, including redirect validation. Only
// the test's transport dials a loopback TLS server trusted by its own test CA;
// production certificate validation and host restrictions are not changed.
func useWindowsReleaseTestServer(t *testing.T, handler http.HandlerFunc, offline bool) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "github.com:443" && address != "release-assets.githubusercontent.com:443" && address != "objects.githubusercontent.com:443" {
			t.Errorf("production URL guard attempted a non-release connection: %s", address)
			return nil, errors.New("unexpected non-release connection")
		}
		if offline {
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("synthetic offline transport")}
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections(); server.Close() })
}

func TestWindowsFailedDownloadsPreserveInstalledVersionHistoryAndQueue(t *testing.T) {
	for _, mode := range []string{"offline", "http-error", "cancelled-before-request", "cancelled-mid-transfer", "truncated-transfer", "checksum-mismatch", "duplicate-checksum", "invalid-zip-crc", "wrong-package-version", "payload-hash-mismatch", "oversized-response", "untrusted-redirect"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := windowsPayloadFixture(t, "v0.4.0-download-old")
			b, _ := windowsPayloadFixture(t, "v0.4.0-download-new")
			state := filepath.Join(t.TempDir(), "state")
			h := &windowsDistributionHarness{}
			if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
				t.Fatal(err)
			}
			windowsSeedDurableHistoryAndQueue(t, state)
			stageParent := filepath.Join(state, "download-stage")
			root, err := openStateRoot(stageParent, true)
			if err != nil {
				t.Fatal(err)
			}
			root.Close()
			before := windowsInstallationSnapshot(t, state)
			if mode == "wrong-package-version" {
				b, _ = windowsPayloadFixture(t, "v0.4.0-other")
			}
			if mode == "payload-hash-mismatch" {
				if err := os.WriteFile(filepath.Join(b, "laodi.exe"), []byte("changed synthetic binary"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			archive := zipFixture(t, b, nil)
			if mode == "invalid-zip-crc" {
				reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
				if err != nil {
					t.Fatal(err)
				}
				offset, err := reader.File[0].DataOffset()
				if err != nil {
					t.Fatal(err)
				}
				archive[offset] ^= 0xff
			}
			want := serviceHash(archive)
			if mode == "checksum-mismatch" {
				want = strings.Repeat("0", 64)
			}
			asset := "Laodi-skills-v0.4.0-download-new-windows-amd64.zip"
			checksum := want + "  " + asset + "\n"
			if mode == "duplicate-checksum" {
				checksum += checksum
			}
			transferStarted := make(chan struct{})
			useWindowsReleaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "github.com" {
					t.Errorf("official host was changed: %s", r.Host)
				}
				if mode == "http-error" {
					http.Error(w, "synthetic unavailable", http.StatusServiceUnavailable)
					return
				}
				if mode == "untrusted-redirect" {
					http.Redirect(w, r, "https://untrusted.invalid/package", http.StatusFound)
					return
				}
				if strings.HasSuffix(r.URL.Path, "SHA256SUMS-windows") {
					fmt.Fprint(w, checksum)
					return
				}
				switch mode {
				case "cancelled-mid-transfer":
					w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
					w.Write(archive[:len(archive)/2])
					w.(http.Flusher).Flush()
					close(transferStarted)
					<-r.Context().Done()
				case "truncated-transfer":
					w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
					w.Write(archive[:len(archive)/2])
				case "oversized-response":
					w.Header().Set("Content-Length", fmt.Sprint(maxDistributionTotal+1))
					w.WriteHeader(http.StatusOK)
				default:
					w.Write(archive)
				}
			}, mode == "offline")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if mode == "cancelled-before-request" {
				cancel()
			}
			type outcome struct {
				dir     string
				cleanup func()
				err     error
			}
			done := make(chan outcome, 1)
			go func() {
				dir, cleanup, err := DownloadWindowsRelease(ctx, "v0.4.0-download-new", stageParent)
				done <- outcome{dir, cleanup, err}
			}()
			if mode == "cancelled-mid-transfer" {
				select {
				case <-transferStarted:
					cancel()
				case <-ctx.Done():
					t.Fatal("transfer did not reach cancellation boundary")
				}
			}
			result := <-done
			if result.cleanup != nil {
				result.cleanup()
			}
			if result.err == nil || result.dir != "" {
				t.Fatalf("failed download became installable: dir=%q err=%v", result.dir, result.err)
			}
			if (mode == "cancelled-before-request" || mode == "cancelled-mid-transfer") && !errors.Is(result.err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", result.err)
			}
			if after := windowsInstallationSnapshot(t, state); !reflect.DeepEqual(before, after) {
				t.Fatal("failed download changed installed files, history, HMAC identity, queue or left its stage")
			}
			if current, err := readWindowsCurrent(state); err != nil || current.Manifest.Version != "v0.4.0-download-old" {
				t.Fatalf("old selected version unavailable: %v", err)
			}
		})
	}
}

func TestWindowsDownloadRejectsStreamingBodyBeyondLimit(t *testing.T) {
	useWindowsReleaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		fmt.Fprint(w, strings.Repeat("x", 65))
	}, false)
	if _, err := officialWindowsDownload(context.Background(), "https://github.com/Shenrui-Ma/Laodi-skills/releases/test", 64); err == nil {
		t.Fatal("chunked body exceeded its byte budget")
	}
}

func TestWindowsDownloadRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		allowed      bool
	}{
		{"official-assets", "https://release-assets.githubusercontent.com/target", true},
		{"http-downgrade", "http://github.com/target", false},
		{"external-host", "https://untrusted.invalid/target", false},
		{"credentials", "https://synthetic:invalid@github.com/target", false},
		{"explicit-port", "https://github.com:443/target", false},
		{"redirect-loop", "https://github.com/initial", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useWindowsReleaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target" {
					if !tc.allowed {
						t.Error("disallowed redirect was followed")
					}
					fmt.Fprint(w, "synthetic verified destination")
					return
				}
				http.Redirect(w, r, tc.target, http.StatusFound)
			}, false)
			data, err := officialWindowsDownload(context.Background(), "https://github.com/initial", 1024)
			if tc.allowed {
				if err != nil || string(data) != "synthetic verified destination" {
					t.Fatalf("official redirect rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe or unbounded redirect accepted")
			}
		})
	}
}

func TestWindowsVerifiedDownloadInstallsAndCleansOnlyItsOwnStage(t *testing.T) {
	a, _ := windowsPayloadFixture(t, "v0.4.0-download-old")
	b, bm := windowsPayloadFixture(t, "v0.4.0-download-new")
	state := filepath.Join(t.TempDir(), "state")
	h := &windowsDistributionHarness{}
	if _, err := InstallDistribution(h.plan(t, a, state)); err != nil {
		t.Fatal(err)
	}
	windowsSeedDurableHistoryAndQueue(t, state)
	before := windowsInstallationSnapshot(t, state)
	archive := zipFixture(t, b, nil)
	useWindowsReleaseTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS-windows") {
			fmt.Fprintf(w, "%s  Laodi-skills-%s-windows-amd64.zip\n", serviceHash(archive), bm.Version)
			return
		}
		w.Write(archive)
	}, false)
	stageParent := filepath.Join(state, "download-stage")
	stage, cleanup, err := DownloadWindowsRelease(context.Background(), bm.Version, stageParent)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if current, err := readWindowsCurrent(state); err != nil || current.Manifest.Version != "v0.4.0-download-old" {
		t.Fatal("download switched the installed version before installation")
	}
	if err := writeWindowsBytes(stageParent, "unrelated.json", []byte("synthetic unrelated stage marker"), false); err != nil {
		t.Fatal(err)
	}
	if result, err := InstallDistribution(h.plan(t, stage, state)); err != nil || !result.Updated {
		t.Fatalf("verified download did not install: %+v %v", result, err)
	}
	cleanup()
	cleanup()
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned stage was not removed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(stageParent, "unrelated.json")); err != nil || string(data) != "synthetic unrelated stage marker" {
		t.Fatal("cleanup removed a sibling stage entry")
	}
	if current, err := readWindowsCurrent(state); err != nil || current.Manifest.Version != bm.Version {
		t.Fatalf("selected downloaded version invalid: %v", err)
	}
	assertWindowsDurableSnapshot(t, state, before)
}
