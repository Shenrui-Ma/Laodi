package laodi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This opt-in contract test uses the exact original producer segments prepared
// from the audited ASAR. The independent Go receiver decrypts and inspects the
// archive. No official endpoint, account, desktop or real repository is used.
func TestWindowsArchiveOriginalProducerABA(t *testing.T) {
	node, module, app := os.Getenv("LAODI_ARCHIVE_NODE"), os.Getenv("LAODI_ARCHIVE_COMPONENT"), os.Getenv("LAODI_WINDOWS_ARCHIVE_APP")
	if node == "" || module == "" || app == "" {
		t.Skip("requires prepared original public producer and Node")
	}
	if _, _, err := inspectProtectionBundle(app); err != nil {
		t.Fatal(err)
	}
	f := newWindowsGuardFixture(t, false)
	git := os.Getenv("LAODI_TEST_GIT")
	if git == "" {
		var err error
		git, err = exec.LookPath("git.exe")
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(f.home, "合成 repository")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		c := exec.Command(git, args...)
		c.Dir = repo
		c.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "PATH=" + filepath.Dir(git) + ";" + filepath.Join(os.Getenv("SystemRoot"), "System32"), "HOME=" + f.home, "USERPROFILE=" + f.home, "TEMP=" + f.home, "TMP=" + f.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(f.home, "absent-gitconfig")}
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v %s", err, out)
		}
	}
	runGit("init", "--quiet")
	runGit("config", "user.name", "Synthetic")
	runGit("config", "user.email", "fixture@example.invalid")
	const historyCanary = "LAODI_SYNTHETIC_HISTORY_ONLY_20260921"
	if err := os.WriteFile(filepath.Join(repo, "removed-history.txt"), []byte(historyCanary), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "--quiet", "-m", "synthetic history")
	runGit("rm", "--quiet", "removed-history.txt")
	runGit("commit", "--quiet", "-m", "remove from working tree")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("synthetic current workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	var accepted, historyFound, extraFound atomic.Int32
	var receiverFailure atomic.Bool
	var receiverMu sync.Mutex
	var firstCiphertext []byte
	reconstructed := map[string][]byte{}
	var incrementFound atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receiverMu.Lock()
		defer receiverMu.Unlock()
		var body struct {
			Ciphertext string `json:"ciphertext"`
			Envelope   struct {
				EncryptedDataKey string `json:"encryptedDataKey"`
				PlaintextSHA256  string `json:"plaintextSha256"`
				AAD              struct {
					Kind string `json:"kind"`
				} `json:"aad"`
			} `json:"envelope"`
		}
		fail := func() { receiverFailure.Store(true); http.Error(w, "invalid synthetic archive", 400) }
		if json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&body) != nil {
			fail()
			return
		}
		wrapped, e := base64.StdEncoding.DecodeString(body.Envelope.EncryptedDataKey)
		if e != nil {
			fail()
			return
		}
		secret, e := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, wrapped, nil)
		if e != nil {
			fail()
			return
		}
		encrypted, e := base64.StdEncoding.DecodeString(body.Ciphertext)
		if e != nil || len(encrypted) <= 16 {
			fail()
			return
		}
		if accepted.Load() == 0 {
			firstCiphertext = append([]byte(nil), encrypted...)
		}
		if accepted.Load() == 1 && !bytes.Equal(firstCiphertext, encrypted) {
			fail()
			return
		}
		block, e := aes.NewCipher(secret)
		if e != nil {
			fail()
			return
		}
		plain := make([]byte, len(encrypted)-16)
		cipher.NewCTR(block, encrypted[:16]).XORKeyStream(plain, encrypted[16:])
		hash := sha256.Sum256(plain)
		if hex.EncodeToString(hash[:]) != body.Envelope.PlaintextSHA256 {
			fail()
			return
		}
		gz, e := gzip.NewReader(bytes.NewReader(plain))
		if e != nil {
			fail()
			return
		}
		defer gz.Close()
		tr := tar.NewReader(io.LimitReader(gz, 32<<20))
		hasGit, hasHistory, hasExtra := false, false, false
		files := map[string][]byte{}
		var manifest struct {
			Files []struct {
				Path string `json:"path"`
				Size int64  `json:"sizeBytes"`
			} `json:"files"`
		}
		var delta struct {
			Schema string `json:"schema"`
			Added  []struct {
				Path string `json:"path"`
				Size int64  `json:"sizeBytes"`
			} `json:"addedOrModified"`
			Deleted []string `json:"deleted"`
		}
		for {
			header, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				fail()
				return
			}
			data, e := io.ReadAll(io.LimitReader(tr, 2<<20))
			if e != nil {
				fail()
				return
			}
			if strings.HasSuffix(header.Name, "/meta/manifest.json") {
				if json.Unmarshal(data, &manifest) != nil {
					fail()
					return
				}
			}
			if strings.HasSuffix(header.Name, "/meta/delta.json") {
				if json.Unmarshal(data, &delta) != nil {
					fail()
					return
				}
			}
			if _, relative, found := strings.Cut(header.Name, "/files/"); found {
				if _, duplicate := files[relative]; duplicate {
					fail()
					return
				}
				files[relative] = data
			}
			if strings.Contains(header.Name, "/files/.git/") {
				hasGit = true
			}
			if strings.Contains(header.Name, "/files/.git/objects/") {
				if zr, e := zlib.NewReader(bytes.NewReader(data)); e == nil {
					object, _ := io.ReadAll(io.LimitReader(zr, 2<<20))
					zr.Close()
					hasHistory = hasHistory || bytes.Contains(object, []byte(historyCanary))
				}
			}
			if strings.Contains(header.Name, "/extra-files/") && bytes.Contains(data, []byte("SYNTHETIC_EXTRA_CONFIG")) {
				hasExtra = true
			}
		}
		if !hasGit || !hasExtra {
			fail()
			return
		}
		if body.Envelope.AAD.Kind == "increment" {
			if delta.Schema != "repo_snapshot_delta/v2" || len(delta.Added) != len(files) || string(files["new.txt"]) != "synthetic increment" {
				fail()
				return
			}
			for _, entry := range delta.Added {
				if data, ok := files[entry.Path]; !ok || int64(len(data)) != entry.Size {
					fail()
					return
				}
			}
			deletedReadme := false
			for _, deleted := range delta.Deleted {
				delete(reconstructed, deleted)
				deletedReadme = deletedReadme || deleted == "README.md"
			}
			if !deletedReadme {
				fail()
				return
			}
			incrementFound.Add(1)
		} else if body.Envelope.AAD.Kind != "baseline" || delta.Schema != "" {
			fail()
			return
		} else {
			reconstructed = map[string][]byte{}
		}
		for name, data := range files {
			reconstructed[name] = data
		}
		if len(manifest.Files) != len(reconstructed) {
			fail()
			return
		}
		for _, entry := range manifest.Files {
			if data, ok := reconstructed[entry.Path]; !ok || int64(len(data)) != entry.Size {
				fail()
				return
			}
		}
		if body.Envelope.AAD.Kind == "increment" {
			preservedHistory := false
			for name, data := range reconstructed {
				if strings.HasPrefix(name, ".git/objects/") {
					if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
						object, _ := io.ReadAll(io.LimitReader(zr, 2<<20))
						zr.Close()
						preservedHistory = preservedHistory || bytes.Contains(object, []byte(historyCanary))
					}
				}
			}
			if !preservedHistory {
				fail()
				return
			}
		}
		if hasHistory {
			historyFound.Add(1)
		}
		extraFound.Add(1)
		accepted.Add(1)
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	driver, err := filepath.Abs(filepath.Join("..", "..", "scripts", "dev", "windows_archive_component.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"workspace": repo, "result": filepath.Join(f.home, "baseline-result.json"), "group": "synthetic-baseline", "publicKey": public, "receiver": receiver.URL}
	run := func(action string, denied bool) {
		t.Helper()
		inputPath := filepath.Join(f.home, "component-input.json")
		b, _ := json.Marshal(input)
		if err := os.WriteFile(inputPath, b, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, node, driver, module, inputPath, action)
		c.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "PATH=" + filepath.Dir(git) + ";" + filepath.Join(os.Getenv("SystemRoot"), "System32"), "HOME=" + f.home, "USERPROFILE=" + f.home, "TEMP=" + f.home, "TMP=" + f.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(f.home, "absent-gitconfig")}
		out, e := c.CombinedOutput()
		var result struct {
			OK   bool   `json:"ok"`
			Code string `json:"code"`
		}
		if json.Unmarshal(bytes.TrimSpace(out), &result) != nil {
			t.Fatalf("component output: %v %s", e, out)
		}
		if denied {
			if e == nil || result.OK || (result.Code != "EACCES" && result.Code != "EPERM") {
				t.Fatalf("expected native producer denial: %v %s", e, out)
			}
		} else if e != nil || !result.OK {
			t.Fatalf("component failed: %v %s", e, out)
		}
	}
	run("create", false)
	run("send", false)
	if accepted.Load() != 1 || historyFound.Load() != 1 {
		t.Fatal("positive baseline did not contain history-only data")
	}
	f.enable(t)
	for i := 0; i < 3; i++ {
		run("send", true)
	}
	input["group"] = "synthetic-blocked"
	input["result"] = filepath.Join(f.home, "blocked-result.json")
	run("create", true)
	newRepo := filepath.Join(f.home, "new-project")
	if err := os.Mkdir(newRepo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newRepo, "README.md"), []byte("synthetic new workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	input["workspace"] = newRepo
	run("create", true)
	if accepted.Load() != 1 || receiverFailure.Load() {
		t.Fatal("protected phase reached or corrupted receiver")
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal(err)
	}
	input["workspace"] = repo
	input["result"] = filepath.Join(f.home, "baseline-result.json")
	run("send", false)
	if historyFound.Load() != 2 {
		t.Fatal("same encrypted package did not resume")
	}
	input["previous"] = input["result"]
	input["result"] = filepath.Join(f.home, "increment-result.json")
	input["group"] = "synthetic-increment"
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("synthetic increment"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "README.md")); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "--quiet", "-m", "synthetic increment")
	f.enable(t)
	run("create", true)
	if accepted.Load() != 2 || receiverFailure.Load() {
		t.Fatal("protected increment reached receiver")
	}
	if _, err := DisableArchiveGuard(f.home, f.state); err != nil {
		t.Fatal(err)
	}
	run("create", false)
	run("send", false)
	if incrementFound.Load() != 1 {
		t.Fatal("increment was not reconstructed against baseline")
	}
	if accepted.Load() != 3 || extraFound.Load() != 3 || receiverFailure.Load() {
		t.Fatal("restored incremental upload did not pass independent receiver")
	}
	t.Log("original producer: baseline received with Git history and extra config; 3 synthetic pending sends + existing/new workspace and increment denied; identical pending ciphertext and increment received after restore")
}
