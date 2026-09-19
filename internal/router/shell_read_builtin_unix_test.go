//go:build unix

package router

import (
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestShellReadOwnedFileIsCloseOnExec(t *testing.T) {
	original, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	owned, err := shellReadOwnedFile(original)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	var flags int
	raw, err := owned.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Control(func(fd uintptr) {
		flags, err = unix.FcntlInt(fd, syscall.F_GETFD, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("duplicated read descriptor is inheritable")
	}
}
