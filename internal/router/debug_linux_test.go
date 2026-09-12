package router

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

// A child process keeps the real filesystem fault and signal handling isolated
// from other tests. No production writer replacement is needed.
func TestDebugInitialWriteFailure(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_DEBUG_WRITE_FAILURE") != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDebugInitialWriteFailure$")
		command.Env = append(os.Environ(), "MEKUGI_TEST_DEBUG_WRITE_FAILURE=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("initial-write subprocess: %v\n%s", err, output)
		}
		return
	}
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	t.Setenv(capturer.AXReadOutputEnvironment, "")
	signal.Ignore(syscall.SIGXFSZ)
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	limit := original
	limit.Cur = 0
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	flags := newRouterFlags(io.Discard)
	*flags.debug = true
	debug, initErr := openDebugOutput(flags)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	if debug != nil || !errors.Is(initErr, syscall.EFBIG) || !strings.Contains(initErr.Error(), "initialize debug artifacts") {
		t.Fatalf("startup did not reject failed first write: %v, %v", debug, initErr)
	}
	logs, err := filepath.Glob(filepath.Join(directory, "mekugi-debug-*", "router.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("startup did not reach initial event: %v, %v", logs, err)
	}
	info, err := os.Stat(logs[0])
	if err != nil || info.Size() != 0 {
		t.Fatalf("first event unexpectedly written: %v, %v", info, err)
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err == nil && strings.HasPrefix(target, directory+string(os.PathSeparator)) {
			t.Fatalf("debug initialization leaked open artifact %s", target)
		}
	}
}
