package laodi

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func protectionClientTestDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("native application-bundle archive protection is macOS-only")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func protectionClientTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProtectionBundleVersionUsesOnlyTopLevelIdentity(t *testing.T) {
	for _, document := range []string{
		`<plist><dict><key>CFBundleVersion</key><string>` + KnownBuild + `</string></dict></plist>`,
		`<?xml version="1.0"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Nested</key><dict><key>CFBundleVersion</key><string>ignored</string></dict><key>CFBundleName</key><string>ZCode</string><!-- identity --><key>CFBundleVersion</key><string>` + KnownBuild + `</string></dict></plist>`,
	} {
		version, err := protectionBundleVersion([]byte(document))
		if err != nil || version != KnownBuild {
			t.Fatalf("version=%q, err=%v", version, err)
		}
	}
}

func TestProtectionBundleVersionRejectsAmbiguousIdentity(t *testing.T) {
	valid := `<plist><dict><key>CFBundleVersion</key><string>` + KnownBuild + `</string></dict></plist>`
	for name, document := range map[string]string{
		"empty":          "",
		"missing":        `<plist><dict/></plist>`,
		"nested-only":    `<plist><dict><key>Nested</key><dict><key>CFBundleVersion</key><string>` + KnownBuild + `</string></dict></dict></plist>`,
		"missing-value":  `<plist><dict><key>CFBundleVersion</key></dict></plist>`,
		"empty-value":    `<plist><dict><key>CFBundleVersion</key><string/></dict></plist>`,
		"non-string":     `<plist><dict><key>CFBundleVersion</key><integer>1</integer></dict></plist>`,
		"wrong-root":     `<other><dict><key>CFBundleVersion</key><string>1</string></dict></other>`,
		"wrong-dict":     `<plist><array><key>CFBundleVersion</key><string>1</string></array></plist>`,
		"duplicate":      strings.Replace(valid, `</dict>`, `<key>CFBundleVersion</key><string>`+KnownBuild+`</string></dict>`, 1),
		"second-root":    valid + valid,
		"second-dict":    strings.Replace(valid, `</plist>`, `<dict/></plist>`, 1),
		"truncated":      strings.TrimSuffix(valid, `</plist>`),
		"undefined-xml":  strings.Replace(valid, KnownBuild, `&undefined;`, 1),
		"nested-value":   strings.Replace(valid, KnownBuild, `<dict/>`+KnownBuild, 1),
		"nested-key":     strings.Replace(valid, `CFBundleVersion`, `<string/>CFBundleVersion`, 1),
		"namespace-root": strings.Replace(valid, `<plist>`, `<plist xmlns="urn:not-apple-plist">`, 1),
		"outer-text":     "unexpected text" + valid,
	} {
		t.Run(name, func(t *testing.T) {
			if version, err := protectionBundleVersion([]byte(document)); err == nil {
				t.Fatalf("accepted malformed identity: %q", version)
			}
		})
	}
}

func TestProtectionBundleRejectsUnsupportedBuildAndArchive(t *testing.T) {
	dir := protectionClientTestDir(t)
	app := filepath.Join(dir, "Client.app")
	resources := filepath.Join(app, "Contents", "Resources")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(app, "Contents", "Info.plist")
	archive := filepath.Join(resources, "app.asar")
	protectionClientTestFile(t, archive, "synthetic unverified archive")
	for _, item := range []struct{ build, errorText string }{
		{KnownBuild + ".changed", "build is not supported"},
		{KnownBuild, "archive differs"},
	} {
		protectionClientTestFile(t, plist, `<plist><dict><key>CFBundleVersion</key><string>`+item.build+`</string></dict></plist>`)
		build, digest, err := inspectProtectionBundle(app)
		if err == nil || !strings.Contains(err.Error(), item.errorText) || build != "" || digest != "" {
			t.Fatalf("build=%q digest=%q err=%v", build, digest, err)
		}
	}
	protectionClientTestFile(t, plist, strings.Repeat("x", (1<<20)+1))
	if _, _, err := inspectProtectionBundle(app); err == nil || !strings.Contains(err.Error(), "safely read application identity") {
		t.Fatalf("oversized identity was not rejected: %v", err)
	}
}

func TestProtectionPathRequiresCleanAbsoluteExpectedType(t *testing.T) {
	dir := protectionClientTestDir(t)
	file := filepath.Join(dir, "regular")
	protectionClientTestFile(t, file, "fixture")
	for _, item := range []struct {
		name, path                string
		directory, missing, valid bool
	}{
		{"directory", dir, true, false, true},
		{"file", file, false, false, true},
		{"missing-allowed", filepath.Join(dir, "absent", "child"), false, true, true},
		{"missing-required", filepath.Join(dir, "absent"), false, false, false},
		{"file-as-directory", file, true, false, false},
		{"directory-as-file", dir, false, false, false},
		{"root-as-file", string(filepath.Separator), false, false, false},
		{"file-parent", filepath.Join(file, "child"), false, true, false},
		{"relative", "relative", true, true, false},
		{"empty", "", true, true, false},
		{"dot-component", dir + string(filepath.Separator) + ".", true, false, false},
		{"double-separator", dir + string(filepath.Separator) + string(filepath.Separator) + "regular", false, false, false},
		{"newline", dir + "\nmissing", true, true, false},
		{"carriage-return", dir + "\rmissing", true, true, false},
		{"nul", dir + "\x00missing", true, true, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			if err := checkProtectionPath(item.path, item.directory, item.missing); (err == nil) != item.valid {
				t.Fatalf("valid=%t err=%v", item.valid, err)
			}
		})
	}
}

func TestProtectionFileRejectsSymlinkComponentsAndSockets(t *testing.T) {
	dir := protectionClientTestDir(t)
	file := filepath.Join(dir, "regular")
	protectionClientTestFile(t, file, "fixture")
	for _, item := range []struct{ name, target, suffix string }{
		{"file-link", file, ""},
		{"parent-link", dir, "regular"},
		{"dangling-link", filepath.Join(dir, "absent"), ""},
	} {
		t.Run(item.name, func(t *testing.T) {
			link := filepath.Join(dir, item.name)
			if err := os.Symlink(item.target, link); err != nil {
				if runtime.GOOS == "windows" {
					t.Skip("symbolic link creation unavailable")
				}
				t.Fatal(err)
			}
			if item.suffix != "" {
				link = filepath.Join(link, item.suffix)
			}
			if err := checkProtectionPath(link, false, true); err == nil {
				t.Fatal("symbolic link accepted, including with allowMissing")
			}
			if opened, err := openProtectionFile(link, 100); err == nil {
				opened.Close()
				t.Fatal("symbolic link opened")
			}
		})
	}
	if runtime.GOOS == "windows" {
		return
	}
	// A short temporary name stays within macOS's Unix socket path budget.
	short, err := os.MkdirTemp("", "laodi-pc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	short, err = filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(short, "socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if opened, err := openProtectionFile(socket, 100); err == nil {
		opened.Close()
		t.Fatal("special file opened")
	}
}

func TestProtectionFileReadBounds(t *testing.T) {
	dir := protectionClientTestDir(t)
	file := filepath.Join(dir, "bounded")
	for _, item := range []struct {
		content string
		limit   int64
		valid   bool
	}{
		{"", 0, true}, {"a", 0, false}, {"1234", 4, true}, {"12345", 4, false}, {"", -1, false},
	} {
		protectionClientTestFile(t, file, item.content)
		actual, err := readProtectionFile(file, item.limit)
		if (err == nil) != item.valid || (item.valid && string(actual) != item.content) {
			t.Fatalf("length=%d limit=%d actual=%q err=%v", len(item.content), item.limit, actual, err)
		}
	}
	large, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(maxProtectionASARBytes + 1); err != nil {
		large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := openProtectionFile(file, maxProtectionASARBytes); err == nil {
		opened.Close()
		t.Fatal("oversized sparse archive opened")
	}
}

func TestProtectionFileDescriptorSurvivesPathReplacement(t *testing.T) {
	dir := protectionClientTestDir(t)
	file := filepath.Join(dir, "identity")
	protectionClientTestFile(t, file, "original")
	opened, err := openProtectionFile(file, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	replacement := filepath.Join(dir, "replacement")
	protectionClientTestFile(t, replacement, "replacement")
	if err := os.Rename(replacement, file); err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(opened)
	if err != nil || string(actual) != "original" {
		t.Fatalf("opened descriptor followed replaced pathname: %q err=%v", actual, err)
	}
}

func TestProtectionFileRefusesConcurrentSymlinkReplacement(t *testing.T) {
	dir := protectionClientTestDir(t)
	file, outside := filepath.Join(dir, "target"), filepath.Join(dir, "outside")
	protectionClientTestFile(t, file, "inside")
	protectionClientTestFile(t, outside, "outside-must-never-be-read")
	probe := filepath.Join(dir, "symlink-probe")
	if err := os.Symlink(outside, probe); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic link creation unavailable")
		}
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan error, 1)
	var once sync.Once
	finish := func() { once.Do(func() { close(stop) }) }
	t.Cleanup(finish)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			next := filepath.Join(dir, "next")
			if err := os.Symlink(outside, next); err != nil {
				done <- err
				return
			}
			if err := os.Rename(next, file); err != nil {
				done <- err
				return
			}
			if err := os.WriteFile(next, []byte("inside"), 0600); err != nil {
				done <- err
				return
			}
			if err := os.Rename(next, file); err != nil {
				done <- err
				return
			}
		}
	}()
	for range 300 {
		actual, err := readProtectionFile(file, 100)
		if err == nil && string(actual) != "inside" {
			finish()
			<-done
			t.Fatalf("unsafe concurrent path content read: %q", actual)
		}
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProtectionProcessParsingIsBoundedToClientExecutables(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("application-bundle process paths are specific to macOS protection")
	}
	const app = "/Applications/ZCode.app"
	data := []byte(" 123 " + app + "/Contents/MacOS/ZCode\n" +
		"45\t" + app + "/Contents/Frameworks/ZCode Helper.app/Contents/MacOS/ZCode Helper\n" +
		"87 /Applications/OtherZCode.app/Contents/MacOS/ZCode\n" +
		"88 /Applications/ZCode.app.bak/Contents/MacOS/ZCode\n" +
		"123 " + app + "/Contents/MacOS/ZCode\n" +
		"19 /usr/bin/cat\n" +
		"15030 ZCode\n15030 ZCode\n15031 NotZCode\n15032 ZCode Helper\n" +
		"0 ZCode\n-1 ZCode\ninvalid ZCode\n999999999999999999999999999999 ZCode\n" +
		"42\n43 " + app + "/ContentsEvil/MacOS/ZCode\n44 " + app + "/Contents\n")
	if actual := parseProtectionProcesses(data, app); !reflect.DeepEqual(actual, []int{45, 87, 123, 15030, 15032}) {
		t.Fatalf("wrong process selection: %v", actual)
	}
	spacedApp := "/Applications/Test Apps/ZCode.app"
	if actual := parseProtectionProcesses([]byte("12 "+spacedApp+"/Contents/MacOS/ZCode\n"), spacedApp); !reflect.DeepEqual(actual, []int{12}) {
		t.Fatalf("application path with spaces was split: %v", actual)
	}
	if actual := parseProtectionProcesses(nil, app); actual == nil || len(actual) != 0 {
		t.Fatalf("empty process listing should return empty collection: %v", actual)
	}
}

func TestProtectionProcessOutputRejectsOverflowWithoutPartialWrite(t *testing.T) {
	var output protectionProcessOutput
	budget := bytes.Repeat([]byte{'x'}, 1<<20)
	if n, err := output.Write(budget); err != nil || n != len(budget) {
		t.Fatalf("exact budget rejected: n=%d err=%v", n, err)
	}
	if n, err := output.Write([]byte("overflow")); err == nil || n != 0 {
		t.Fatalf("overflow accepted: n=%d err=%v", n, err)
	}
	if !bytes.Equal(output.Bytes(), budget) {
		t.Fatal("overflow changed previously captured output")
	}
	var single protectionProcessOutput
	if n, err := single.Write(make([]byte, (1<<20)+1)); err == nil || n != 0 || len(single.Bytes()) != 0 {
		t.Fatalf("single oversized write accepted: n=%d len=%d err=%v", n, len(single.Bytes()), err)
	}
}
