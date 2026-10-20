package diagnostic

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactCancellationAfterFinalRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := 0
	err := readArtifact(ctx, path, true, func(int, map[string]jsontext.Value) { observed++; cancel() })
	if observed != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("observed=%d error=%v", observed, err)
	}
}
