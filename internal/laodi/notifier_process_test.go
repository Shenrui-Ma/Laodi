package laodi

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Re-exec the native test binary; no shell, SDK, or real toast is involved.
func init() {
	if os.Getenv("LAODI_TEST_NOTIFIER") != "1" || len(os.Args) < 2 || os.Args[1] != "--send" {
		return
	}
	out := os.Getenv("LAODI_TEST_NOTIFIER_OUTPUT")
	mode := os.Getenv("LAODI_TEST_NOTIFIER_MODE")
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if mode != "args" {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(out, flags, 0600)
	if err != nil {
		os.Exit(2)
	}
	data := strings.Join(os.Args[1:], "\n") + "\n"
	if mode == "calls" {
		data = "called\n"
	}
	if _, err = f.WriteString(data); err != nil {
		os.Exit(2)
	}
	if err = f.Close(); err != nil {
		os.Exit(2)
	}
	fmt.Print(`{"delivery":"accepted_by_os"}`)
	os.Exit(0)
}

func syntheticNotifier(t *testing.T, output, mode string) string {
	t.Helper()
	t.Setenv("LAODI_TEST_NOTIFIER", "1")
	t.Setenv("LAODI_TEST_NOTIFIER_OUTPUT", output)
	t.Setenv("LAODI_TEST_NOTIFIER_MODE", mode)
	// The synthetic child has no concurrent work. The race runtime's default
	// one-second exit delay would otherwise distort the notifier protocol and
	// consume Watch's deliberately short recovery-test duration.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}
