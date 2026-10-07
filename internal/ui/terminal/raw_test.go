package terminal

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestWithRawPanePreservesSharedTerminal(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	fd := int(slave.Fd())
	// Like inherited stdio, the two descriptors share file-status flags.
	alias, err := unix.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	input := os.NewFile(uintptr(alias), fmt.Sprintf("/dev/fd/%d", alias))
	defer input.Close()
	err = WithRawPane(t.Context(), input, slave, "", "", func(keys <-chan byte) error {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_NONBLOCK != 0 {
			t.Errorf("stdout became nonblocking: flags=%d, error=%v", flags, err)
		}
		if _, err := master.Write([]byte{'x'}); err != nil {
			return err
		}
		select {
		case key := <-keys:
			if key != 'x' {
				t.Errorf("key = %q", key)
			}
		case <-time.After(time.Second):
			return errors.New("terminal key was not delivered")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(alias), unix.F_GETFL, 0); err != nil {
		t.Fatalf("caller descriptor was closed: %v", err)
	}
}
