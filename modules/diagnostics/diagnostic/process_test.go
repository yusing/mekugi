package diagnostic

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessLogBoundedPrivateTailAndWriteFailure(t *testing.T) {
	p, err := OpenProcessLog(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	data := append(bytes.Repeat([]byte("x"), 3*processSegmentBytes), []byte("panic: retained tail\n")...)
	if n, err := p.Write(data); n != len(data) || err != nil {
		t.Fatalf("drain: %d, %v", n, err)
	}
	previous, err := os.ReadFile(filepath.Join(p.directory, "stderr.previous.log"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(p.directory, "stderr.log"))
	if err != nil || len(previous) != processSegmentBytes || !bytes.Equal(append(previous, current...), data[2*processSegmentBytes:]) {
		t.Fatalf("bounded tail: previous=%d current=%q err=%v", len(previous), current, err)
	}
	if err := p.stderr.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Write([]byte("still drain")); n != len("still drain") || err != nil {
		t.Fatalf("storage failure changed stderr consumption: %d, %v", n, err)
	}
	if err := p.Record(ProcessEvent{Event: "exit"}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(p.directory, "process.jsonl"))
	var event ProcessEvent
	if err != nil || json.Unmarshal(bytes.TrimSpace(data), &event) != nil || event.StderrError == "" {
		t.Fatalf("storage failure evidence: %s, %v", data, err)
	}
	for _, name := range []string{"", "process.jsonl", "stderr.log", "stderr.previous.log"} {
		info, err := os.Stat(filepath.Join(p.directory, name))
		want := os.FileMode(0600)
		if name == "" {
			want = 0700
		}
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("private artifact %q: %v, %v", name, info, err)
		}
	}
}

func TestProcessLogCleanupFailureDoesNotSuppressNewEvidence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	root := t.TempDir()
	old, release, err := createBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	lease := filepath.Join(old, debugBundleLease)
	stamp := time.Now().Add(-15 * 24 * time.Hour)
	if err := os.Chtimes(lease, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lease, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(lease, 0600)
	p, err := OpenProcessLog(t.Context(), root)
	if err != nil {
		t.Fatalf("unrelated retention failure suppressed collection: %v", err)
	}
	defer p.Close()
	if p.retentionErr == nil {
		t.Fatal("fixture did not reach retention failure")
	}
	if _, err := p.Write([]byte("retained")); err != nil {
		t.Fatal(err)
	}
	if err := p.Record(ProcessEvent{Event: "start"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.directory, "process.jsonl"))
	var event ProcessEvent
	if err != nil || json.Unmarshal(bytes.TrimSpace(data), &event) != nil || event.RetentionError == "" {
		t.Fatalf("retention failure evidence: %s, %v", data, err)
	}
}
