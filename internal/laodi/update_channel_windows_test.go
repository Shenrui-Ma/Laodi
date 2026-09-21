//go:build windows

package laodi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

const windowsChannelFixture = `{"schema":1,"channel":"recommended","platform":"windows/amd64","version":"v0.4.1-windows.beta.2"}`

func TestWindowsRecommendedChannelStrictSchema(t *testing.T) {
	for name, data := range map[string]string{
		"empty": "", "array": "[]", "null": "null",
		"missing":           `{"schema":1,"channel":"recommended","platform":"windows/amd64"}`,
		"duplicate":         strings.Replace(windowsChannelFixture, `"schema":1`, `"schema":2,"schema":1`, 1),
		"escaped-duplicate": strings.Replace(windowsChannelFixture, `"schema":1`, `"schema":1,"\u0073chema":1`, 1),
		"unknown":           strings.Replace(windowsChannelFixture, `"schema":1`, `"url":"https://example.invalid","schema":1`, 1),
		"case":              strings.Replace(windowsChannelFixture, `"schema"`, `"Schema"`, 1),
		"schema-version":    strings.Replace(windowsChannelFixture, `"schema":1`, `"schema":2`, 1),
		"schema-string":     strings.Replace(windowsChannelFixture, `"schema":1`, `"schema":"1"`, 1),
		"schema-float":      strings.Replace(windowsChannelFixture, `"schema":1`, `"schema":1.0`, 1),
		"wrong-channel":     strings.Replace(windowsChannelFixture, "recommended", "latest", 1),
		"wrong-platform":    strings.Replace(windowsChannelFixture, "windows/amd64", "darwin/arm64", 1),
		"wrong-arch":        strings.Replace(windowsChannelFixture, "windows/amd64", "windows/arm64", 1),
		"mac-tag":           strings.Replace(windowsChannelFixture, "v0.4.1-windows.beta.2", "v0.4.1-beta.1", 1),
		"ambiguous-tag":     strings.Replace(windowsChannelFixture, "windows.beta.2", "windowsish.beta.2", 1),
		"leading-zero":      strings.Replace(windowsChannelFixture, "beta.2", "beta.02", 1),
		"path-tag":          strings.Replace(windowsChannelFixture, "beta.2", "beta.2/other", 1),
		"trailing-object":   windowsChannelFixture + `{}`,
		"trailing-junk":     windowsChannelFixture + `!`,
		"oversized":         windowsChannelFixture + strings.Repeat(" ", maxWindowsChannelBytes),
		"invalid-utf8":      windowsChannelFixture + string([]byte{0xff}),
		"null-version":      strings.Replace(windowsChannelFixture, `"v0.4.1-windows.beta.2"`, `null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if tag, err := parseWindowsRecommendedChannel([]byte(data)); err == nil {
				t.Fatalf("accepted invalid metadata as %s", tag)
			}
		})
	}
	for _, tag := range []string{"v0.4.1-windows.beta.2", "v1.0.0-windows"} {
		data := strings.Replace(windowsChannelFixture, "v0.4.1-windows.beta.2", tag, 1)
		got, err := parseWindowsRecommendedChannel([]byte(data + "\n"))
		if err != nil || got != tag {
			t.Fatalf("valid metadata: %s %v", got, err)
		}
	}
}

func TestWindowsRecommendedChannelRepositoryMetadata(t *testing.T) {
	data, err := os.ReadFile("../../channels/windows-amd64.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseWindowsRecommendedChannel(data); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsReleaseVersionOrdering(t *testing.T) {
	for _, pair := range [][2]string{
		{"v0.4.1-windows.beta.2", "v0.4.1-windows.beta.10"},
		{"v0.4.1-windows.beta.2", "v0.4.2-windows.beta.1"},
		{"v0.4.1-windows.beta.2", "v1.0.0-windows"},
		{"v0.4.1-windows.beta.2", "v0.10.0-windows"},
		{"v0.4.1-windows.beta.2", "v0.4.1"},
		{"v0.4.1-windows.beta.2", "v0.4.1-windows.beta.2.1"},
		{"v0.4.1-windows.beta.2", "v0.4.1-windows.rc.1"},
		{"v0.4.1-windows.beta.99999999999999999999", "v0.4.1-windows.beta.100000000000000000000"},
		{"v0.4.1-windows.2", "v0.4.1-windows.beta"},
	} {
		if compareWindowsReleaseVersions(pair[0], pair[1]) >= 0 || compareWindowsReleaseVersions(pair[1], pair[0]) <= 0 || compareWindowsReleaseVersions(pair[0], pair[0]) != 0 {
			t.Fatalf("incorrect order: %q", pair)
		}
	}
}

func TestWindowsRecommendedChannelNetworkAndDowngrade(t *testing.T) {
	for _, mode := range []string{"success", "same", "development", "newer-installed", "offline", "cancelled", "not-found", "server-error", "redirect-http", "redirect-https", "oversized-header", "oversized-stream", "truncated", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Host != "raw.githubusercontent.com" || r.URL.Path != "/Shenrui-Ma/Laodi/main/channels/windows-amd64.json" || r.URL.RawQuery != "" || r.Method != http.MethodGet {
					t.Errorf("unexpected channel request: %s %s %s", r.Method, r.Host, r.URL)
				}
				switch mode {
				case "not-found":
					w.WriteHeader(404)
				case "server-error":
					w.WriteHeader(503)
				case "redirect-http":
					http.Redirect(w, r, "http://raw.githubusercontent.com/other", 302)
				case "redirect-https":
					http.Redirect(w, r, "https://github.com/other", 302)
				case "oversized-header":
					w.Header().Set("Content-Length", fmt.Sprint(maxWindowsChannelBytes+1))
					fmt.Fprint(w, windowsChannelFixture)
				case "oversized-stream":
					w.(http.Flusher).Flush()
					fmt.Fprint(w, windowsChannelFixture+strings.Repeat(" ", maxWindowsChannelBytes))
				case "truncated":
					w.Header().Set("Content-Length", "1000")
					fmt.Fprint(w, windowsChannelFixture)
				case "invalid-json":
					fmt.Fprint(w, "not JSON")
				default:
					fmt.Fprint(w, windowsChannelFixture)
				}
			}))
			defer server.Close()
			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			pool := x509.NewCertPool()
			pool.AddCert(server.Certificate())
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "raw.githubusercontent.com:443" {
					t.Errorf("unexpected channel destination: %s", address)
					return nil, errors.New("unexpected destination")
				}
				if mode == "offline" {
					return nil, errors.New("synthetic offline")
				}
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			previous := http.DefaultTransport
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			current := "v0.4.1-windows.beta.1"
			switch mode {
			case "same":
				current = "v0.4.1-windows.beta.2"
			case "newer-installed":
				current = "v0.4.1-windows.beta.3"
			case "development":
				current = "0.4.1-dev"
			}
			tag, err := RecommendedWindowsRelease(ctx, current)
			success := mode == "success" || mode == "same" || mode == "development"
			if success && (err != nil || tag != "v0.4.1-windows.beta.2") || !success && (err == nil || tag != "") {
				t.Fatalf("tag=%s error=%v", tag, err)
			}
			if requests > 1 {
				t.Fatalf("followed channel redirect: %d requests", requests)
			}
		})
	}
}
