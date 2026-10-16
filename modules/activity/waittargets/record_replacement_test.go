//go:build unix || js

package waittargets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWaitTargetsRejectSymlinkReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "wait.json")
	regular := filepath.Join(directory, "regular.json")
	target := filepath.Join(directory, "target.json")
	pending := filepath.Join(directory, "pending")
	for name, wire := range map[string]string{
		path: "[]", regular: "[]", target: `[{"thread":"other","name":"unselected"}]`,
	} {
		if err := os.WriteFile(name, []byte(wire), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if err := os.Symlink(target, pending); err != nil {
				done <- err
				return
			}
			if err := os.Rename(pending, path); err != nil {
				done <- err
				return
			}
			if err := os.Link(regular, pending); err != nil {
				done <- err
				return
			}
			if err := os.Rename(pending, path); err != nil {
				done <- err
				return
			}
		}
	}()
	defer func() {
		close(stop)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}()
	for range 10000 {
		targets, found, err := Read(path)
		if err != nil {
			continue // Rejecting a replaced record is safe; following it is not.
		}
		if !found || len(targets) != 0 {
			t.Fatalf("read substituted snapshot: found=%v targets=%+v", found, targets)
		}
	}
}
