package orchestrate

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRetainEvidence(t *testing.T) {
	s := &Store{Directory: t.TempDir()}
	workspace := t.TempDir()
	// Evidence retention consumes an already prepared batch; checkout mechanics
	// are covered by the real MCP launch test.
	err := s.withRun(t.Context(), workspace, "main", func(m *manifest, path string) error {
		m.Batches = []Batch{{TaskName: "batch", State: "prepared", Checkout: filepath.Join(filepath.Dir(path), "run", "batch")}}
		return s.save(m, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Preparation owns the run directory before evidence retention.
	batches, _ := s.Snapshot(workspace, "main")
	if err := os.MkdirAll(filepath.Dir(batches[0].Checkout), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "image.png")
	bytes := []byte{0, 255, 13, 10, 0, 128}
	if err := os.WriteFile(source, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	inputs := []EvidenceInput{{Name: "image.png", Source: source}}
	b, err := s.RetainEvidence(t.Context(), workspace, "main", "batch", inputs)
	if err != nil || len(b.Evidence) != 1 || b.Evidence[0].State != "ready" {
		t.Fatal(b, err)
	}
	for _, path := range []string{source, b.Evidence[0].Path} {
		if got, err := os.ReadFile(path); err != nil || !reflect.DeepEqual(got, bytes) {
			t.Fatal("copy changed bytes", path, got, err)
		}
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	reopened := &Store{Directory: s.Directory}
	repeat, err := reopened.RetainEvidence(t.Context(), workspace, "main", "batch", inputs)
	if err != nil || !reflect.DeepEqual(repeat, b) {
		t.Fatal("repeat replaced retained copy", repeat, err)
	}
	inputs[0].Source = filepath.Join(t.TempDir(), "another.png")
	if _, err := reopened.RetainEvidence(t.Context(), workspace, "main", "batch", inputs); err == nil {
		t.Fatal("reused evidence name with another source")
	}
	if err := os.WriteFile(b.Evidence[0].Path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.RetainEvidence(t.Context(), workspace, "main", "batch", []EvidenceInput{{Name: "image.png", Source: source}}); err == nil {
		t.Fatal("accepted changed retained copy")
	}
	// Independent targets continue after failure, with durable outcomes. A failed
	// copy is not retried from a later source.
	valid := filepath.Join(t.TempDir(), "valid")
	if err := os.WriteFile(valid, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	partial := []EvidenceInput{{Name: "missing", Source: source}, {Name: "valid", Source: valid}}
	b, err = reopened.RetainEvidence(t.Context(), workspace, "main", "batch", partial)
	if err == nil || len(b.Evidence) != 3 || b.Evidence[1].State != "failed" || b.Evidence[2].State != "ready" {
		t.Fatal("independent target outcomes", b, err)
	}
	if err := os.WriteFile(source, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.RetainEvidence(t.Context(), workspace, "main", "batch", partial); err == nil {
		t.Fatal("retried failed copy")
	}
	if _, err := reopened.RetainEvidence(t.Context(), workspace, "main", "batch", []EvidenceInput{{Name: "../escape", Source: valid}}); err == nil {
		t.Fatal("accepted escaping name")
	}
}

func TestEvidenceCopyRejectsStorageEscape(t *testing.T) {
	run, outside := t.TempDir(), t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("input"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(run, ".evidence"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(run, ".evidence", "batch")); err != nil {
		t.Fatal(err)
	}
	if _, err := copyEvidence(t.Context(), source, run, filepath.Join(".evidence", "batch", "input")); err == nil {
		t.Fatal("copied evidence outside run storage")
	}
	if _, err := os.Stat(filepath.Join(outside, "input")); !os.IsNotExist(err) {
		t.Fatal("changed external target", err)
	}
	redirected := filepath.Join(t.TempDir(), "run")
	if err := os.Symlink(outside, redirected); err != nil {
		t.Fatal(err)
	}
	if _, err := copyEvidence(t.Context(), source, redirected, filepath.Join(".evidence", "batch", "input")); err == nil {
		t.Fatal("accepted redirected run storage")
	}
}
