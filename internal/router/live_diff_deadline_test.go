package router

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type delayedPreviewContext struct {
	context.Context
	once sync.Once
}

func (c *delayedPreviewContext) Err() error {
	c.once.Do(func() { time.Sleep(250 * time.Millisecond) })
	return c.Context.Err()
}

func TestInterpreterPreviewUsesProjectionDeadline(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "target.txt")
	const before = "é🙂\n"
	writeTestFile(t, path, before)
	statement := mustShellStatement(t, "python3 - <<'PY'\ndef edit(first, second):\n    open('output.txt', 'w').write(first + '|' + second)\nwith open('target.txt') as handle:\n    edit(handle.read(), handle.read())\nPY\n")
	for _, budget := range []time.Duration{time.Second, 100 * time.Millisecond} {
		t.Run(budget.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), budget)
				defer cancel()
				// Pause evaluation after its deadline has been selected, as scheduling can.
				files, recognized, err := liveDiffInterpreterWrite(&delayedPreviewContext{Context: ctx}, statement, directory, false)
				if budget == time.Second {
					if err != nil || !recognized || len(files) != 1 {
						t.Fatalf("preview abandoned before its deadline: recognized=%t files=%v err=%v", recognized, files, err)
					}
				} else if len(files) != 0 {
					t.Fatalf("expired projection produced files: %v", files)
				}
			})
		})
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != before {
		t.Fatalf("preview changed source: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "output.txt")); !os.IsNotExist(err) {
		t.Fatalf("preview executed the script: %v", err)
	}
}
