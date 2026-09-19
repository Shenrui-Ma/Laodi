//go:build windows

package laodi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"
)

type windowsHookBurstResult struct {
	Saved     bool    `json:"saved"`
	ElapsedMS float64 `json:"elapsed_ms"`
}

func TestWindowsHookBurstChild(t *testing.T) {
	state := os.Getenv("LAODI_TEST_HOOK_BURST_STATE")
	if state == "" {
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	var start [1]byte
	if _, err := io.ReadFull(os.Stdin, start[:]); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	err := SubmitHookInspection(state, inboxInspection("parallel-process-"+os.Getenv("LAODI_TEST_HOOK_BURST_ID")))
	data, _ := json.Marshal(windowsHookBurstResult{Saved: err == nil, ElapsedMS: float64(time.Since(begin)) / float64(time.Millisecond)})
	fmt.Fprintln(os.Stdout, string(data))
}

// Processes synchronize before submission, so no child is silently serialized
// by process startup. Loss is permitted by the 100 ms production deadline but
// must always leave a gap; every successful producer must remain readable.
func TestWindowsConcurrentHookBurstPreservesAcceptedRecordsAndGap(t *testing.T) {
	const processes = 32
	state := filepath.Join(t.TempDir(), "state")
	if err := SubmitHookInspection(state, inboxInspection("seed")); err != nil {
		t.Fatal(err)
	}
	seed, err := ReadHookInbox(state, 32)
	if err != nil {
		t.Fatal(err)
	}
	if err := AckHookInbox(state, seed.Receipts); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type child struct {
		cmd    *exec.Cmd
		input  io.WriteCloser
		reader *bufio.Reader
	}
	children := make([]child, 0, processes)
	defer func() {
		for _, child := range children {
			_ = child.cmd.Process.Kill()
			child.input.Close()
		}
	}()
	for i := 0; i < processes; i++ {
		cmd := exec.Command(exe, "-test.run=^TestWindowsHookBurstChild$")
		cmd.Env = append(os.Environ(), "LAODI_TEST_HOOK_BURST_STATE="+state, "LAODI_TEST_HOOK_BURST_ID="+strconv.Itoa(i))
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child{cmd: cmd, input: input, reader: bufio.NewReader(output)})
	}
	for _, child := range children {
		line, err := child.reader.ReadString('\n')
		if err != nil || line != "ready\n" {
			t.Fatalf("child did not reach barrier: %q %v", line, err)
		}
	}
	for _, child := range children {
		if _, err := child.input.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	saved := 0
	durations := make([]float64, 0, processes)
	for _, child := range children {
		line, err := child.reader.ReadString('\n')
		var result windowsHookBurstResult
		if err != nil || json.Unmarshal([]byte(line), &result) != nil {
			t.Fatalf("invalid child result: %q %v", line, err)
		}
		if result.Saved {
			saved++
		}
		durations = append(durations, result.ElapsedMS)
		if err := child.cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := ReadHookInbox(state, 64)
	if err != nil || len(batch.Receipts) != saved {
		t.Fatalf("accepted observations lost: saved=%d readable=%d error=%v", saved, len(batch.Receipts), err)
	}
	status, err := GetHookInboxStatus(state)
	if err != nil || (saved < processes && !status.HasGap) {
		t.Fatalf("burst loss was silent: saved=%d status=%+v error=%v", saved, status, err)
	}
	sort.Float64s(durations)
	t.Logf("processes=%d saved=%d gap=%t submit_ms p50=%.2f p95=%.2f max=%.2f", processes, saved, status.HasGap, durations[16], durations[30], durations[31])
}

func TestWindowsInboxLockUsesSameIdentityAsManagementLock(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	root, err := openHookInbox(state, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first, err := AcquireLock(root.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	type result struct {
		release func()
		err     error
	}
	acquired := make(chan result, 1)
	go func() { release, err := lockHookInbox(root); acquired <- result{release, err} }()
	// The one checked waiter cannot enter while the existing lock is held.
	select {
	case early := <-acquired:
		if early.release != nil {
			early.release()
		}
		t.Fatalf("waiter did not contend on the existing lock: %v", early.err)
	case <-time.After(30 * time.Millisecond):
	}
	first()
	var second result
	select {
	case second = <-acquired:
	case <-time.After(time.Second):
		t.Fatal("bounded lock waiter failed to return")
	}
	if second.err != nil {
		t.Fatal(second.err)
	}
	defer second.release()
	if err := root.Rename(".lock", ".replaced-lock"); err == nil {
		t.Fatal("active inbox lock identity was replaceable")
	}
	if release, err := AcquireLock(root.Name()); err == nil {
		release()
		t.Fatal("management lock bypassed the inbox holder")
	}
	second.release()
	if err := root.Remove(".lock"); err != nil {
		t.Fatalf("released inbox lock retained a handle: %v", err)
	}
}

// Component timings distinguish security metadata work from durable filesystem
// flushes. These benchmarks do not relax production security or durability.
func BenchmarkWindowsHookInboxNativePhases(b *testing.B) {
	state := filepath.Join(b.TempDir(), "state")
	if err := SubmitHookInspection(state, inboxInspection("seed")); err != nil {
		b.Fatal(err)
	}
	root, err := openHookInbox(state, false)
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()
	b.Run("open-inbox", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			root, err := openHookInbox(state, true)
			if err != nil {
				b.Fatal(err)
			}
			root.Close()
		}
	})
	b.Run("lock", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			release, err := lockHookInbox(root)
			if err != nil {
				b.Fatal(err)
			}
			release()
		}
	})
	b.Run("read-key", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := hookInboxKey(root, false); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("list", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := hookInboxEntries(root); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("create-private", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			f, err := createPrivateFile(root, ".benchmark-create", os.O_WRONLY)
			if err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			f.Close()
			root.Remove(".benchmark-create")
			b.StartTimer()
		}
	})
	b.Run("flush", func(b *testing.B) {
		f, err := createPrivateFile(root, ".benchmark-flush", os.O_WRONLY)
		if err != nil {
			b.Fatal(err)
		}
		defer f.Close()
		defer root.Remove(".benchmark-flush")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			if _, err := f.WriteAt([]byte("synthetic durable observation"), 0); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			if err := f.Sync(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("publish", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			f, err := createPrivateFile(root, ".benchmark-publish", os.O_WRONLY)
			if err != nil {
				b.Fatal(err)
			}
			f.Write([]byte("synthetic"))
			f.Sync()
			f.Close()
			b.StartTimer()
			if err := replaceStateFile(root, ".benchmark-publish", ".benchmark-published"); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			root.Remove(".benchmark-published")
			b.StartTimer()
		}
	})
}
