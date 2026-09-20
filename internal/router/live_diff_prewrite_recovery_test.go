package router

import (
	"context"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type inspectingPreWriteSink struct {
	preWriteTestSink
	inspect func(liveDiffPreview)
}

func (s *inspectingPreWriteSink) PublishPreWrite(ctx context.Context, preview liveDiffPreview) error {
	if !preview.Complete {
		s.inspect(preview)
	}
	return s.preWriteTestSink.PublishPreWrite(ctx, preview)
}

func TestShellPreWriteRecoveredBatch(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sink := &inspectingPreWriteSink{inspect: func(preview liveDiffPreview) {
		if len(preview.Files) != 2 {
			t.Fatalf("recovered batch preview has %d files", len(preview.Files))
		}
		for _, file := range preview.Files {
			if content, err := os.ReadFile(file.BeforePath); err != nil || string(content) != "old\n" {
				t.Fatalf("file written before preview: %q %v", content, err)
			}
			if !strings.Contains(file.Diff, "+new") {
				t.Fatalf("recovered diff: %s", file.Diff)
			}
		}
	}}
	shell, _ := registry.contribution("shell")
	run := func(source string) toolplugin.ExecutionOutput {
		t.Helper()
		result, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
			[]string{"bash", source}, nil, directory, os.Environ(), sink, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	rejected := run(`hpatch one.txt 'type "old" "new"' two.txt 'type "missing" "new"'`)
	if rejected.ExitCode == 0 || len(sink.previews) != 0 {
		t.Fatalf("rejected batch published a projection: %+v", rejected)
	}
	_, tail, ok := strings.Cut(rejected.Stderr, "Recover this rejected edit with hpatch --recover ")
	if !ok {
		t.Fatal(rejected.Stderr)
	}
	handle, _, _ := strings.Cut(tail, ".")
	fixed := run("hpatch --recover " + handle + ` --script 2 'type "missing" "old"'`)
	if fixed.ExitCode != 0 || len(sink.previews) != 2 || !sink.previews[1].Complete {
		t.Fatalf("recovery: %+v; previews=%d", fixed, len(sink.previews))
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if content, err := os.ReadFile(filepath.Join(directory, name)); err != nil || string(content) != "new\n" {
			t.Fatalf("recovered file: %q %v", content, err)
		}
	}
}

func TestShellPreWriteCancellationBeforeCommit(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sink := &inspectingPreWriteSink{inspect: func(liveDiffPreview) { cancel() }}
	shell, _ := registry.contribution("shell")
	_, _ = executeShellTool(ctx, manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", `hpatch file.txt 'type "old" "new"'`}, nil, directory, os.Environ(), sink, nil, nil)
	if content, err := os.ReadFile(path); err != nil || string(content) != "old\n" {
		t.Fatalf("canceled edit wrote file: %q %v", content, err)
	}
	if len(sink.previews) != 2 || !sink.previews[1].Complete {
		t.Fatalf("cancellation did not finish preview: %+v", sink.previews)
	}
}
