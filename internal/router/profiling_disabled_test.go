//go:build !pprof

package router

import (
	"bytes"
	"testing"
)

func TestSessionProfilingDisabled(t *testing.T) {
	var stderr bytes.Buffer
	stop, err := startProfiling(t.Context(), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("normal build: stderr %q", stderr.String())
	}
}
