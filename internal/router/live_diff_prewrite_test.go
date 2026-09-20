package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/shellruntime"
)

type preWriteTestSink struct {
	previews []liveDiffPreview
	err      error
}

func (s *preWriteTestSink) RequestJournal(context.Context, shellJournalCommand) (shellJournalResult, error) {
	return shellJournalResult{}, nil
}
func (s *preWriteTestSink) Publish(context.Context, string) error { return nil }
func (s *preWriteTestSink) Complete(context.Context) error        { return nil }
func (s *preWriteTestSink) PublishPreWrite(_ context.Context, preview liveDiffPreview) error {
	s.previews = append(s.previews, preview)
	return s.err
}

func TestShellPreWriteObserverUsesEvaluatedSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/file.txt"
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &preWriteTestSink{err: errors.New("auxiliary publisher failed")}
	ctx, finish := shellPreWriteObserver(t.Context(), sink, "call")
	result, err := mekugi.ApplyForHostAt(ctx, dir, []mekugi.FileEdit{{
		Path: "file.txt", Script: "append \"after\\n\"",
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.previews) != 1 {
		t.Fatalf("pre-write publications before completion = %d", len(sink.previews))
	}
	preview := sink.previews[0]
	if !preview.Evaluated || preview.Complete || preview.Status != "PRE-WRITE DIFF" || len(preview.Files) != 1 {
		t.Fatalf("pre-write preview = %+v", preview)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "before\nafter\n" {
		t.Fatalf("applied file = %q, %v", got, err)
	}
	if !strings.Contains(preview.Files[0].UnifiedDiff(), " before") ||
		!strings.Contains(preview.Files[0].UnifiedDiff(), "+after") {
		t.Fatalf("preview did not preserve original-to-final snapshot: %q", preview.Files[0].UnifiedDiff())
	}
	if len(result.ReviewFiles) != 1 || result.ReviewFiles[0].Diff != preview.Files[0].Diff {
		t.Fatal("callback did not receive the actual evaluated review result")
	}

	finish()
	if len(sink.previews) != 2 || !sink.previews[1].Complete ||
		sink.previews[1].Files[0].Diff != preview.Files[0].Diff {
		t.Fatalf("completion did not carry the full snapshot: %+v", sink.previews)
	}
}

func TestShellPreWriteObserverFailureAndNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/file.txt", []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("failed evaluation", func(t *testing.T) {
		sink := new(preWriteTestSink)
		ctx, finish := shellPreWriteObserver(t.Context(), sink, "bad")
		_, err := mekugi.ApplyForHostAt(ctx, dir, []mekugi.FileEdit{{
			Path: "file.txt", Script: `type 1:0000 "after"`,
		}}, "")
		finish()
		if err == nil || len(sink.previews) != 0 {
			t.Fatalf("err=%v previews=%+v", err, sink.previews)
		}
	})
	t.Run("no-op", func(t *testing.T) {
		sink := new(preWriteTestSink)
		ctx, finish := shellPreWriteObserver(t.Context(), sink, "noop")
		if _, err := mekugi.ApplyForHostAt(ctx, dir, []mekugi.FileEdit{{Path: "file.txt"}}, ""); err != nil {
			t.Fatal(err)
		}
		finish()
		if len(sink.previews) != 2 || sink.previews[0].Status != "PRE-WRITE DIFF · no changes" ||
			!sink.previews[1].Complete {
			t.Fatalf("no-op previews = %+v", sink.previews)
		}
	})
}

func TestBoundLiveDiffPreviewUsesBoundedUTF8DiffText(t *testing.T) {
	large := strings.Repeat("世界", 30000) + "\n"
	preview := boundLiveDiffPreview(liveDiffPreview{
		ID: "large", Evaluated: true, Files: []mekugi.ReviewFile{{
			BeforePath: "large.txt", AfterPath: "large.txt",
			Diff: "modify large.txt\n--- large.txt\n+++ large.txt\n@@ -1 +1 @@\n-old\n+" + large,
		}},
	})
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 48<<10 || !preview.DiffText || !preview.Truncated ||
		len(preview.Files) != 0 || preview.Input == "" || !utf8.ValidString(preview.Input) {
		t.Fatalf("bounded preview: json=%d preview=%+v", len(encoded), preview)
	}
}

func TestPreWriteHTTPDerivesAuthenticatedRouteIdentity(t *testing.T) {
	broker := newCommentaryBroker()
	t.Cleanup(broker.close)
	var gotWorkspace, gotThread, gotAuthor string
	var got liveDiffPreview
	broker.previewPublisher = func(workspace, thread, author string, preview liveDiffPreview) {
		gotWorkspace, gotThread, gotAuthor, got = workspace, thread, author, preview
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	t.Cleanup(server.Close)
	token := broker.subscribe("workspace\x00session", "call", "agent")
	broker.bindActivity(token, "thread")
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	if err := sink.PublishPreWrite(t.Context(), liveDiffPreview{
		ID: "preview", Workspace: "spoofed", Thread: "spoofed", Caller: "spoofed",
		Evaluated: true, Status: "PRE-WRITE DIFF",
	}); err != nil {
		t.Fatal(err)
	}
	if gotWorkspace != "workspace" || gotThread != "thread" || gotAuthor != "agent" ||
		got.ID != "preview" {
		t.Fatalf("derived identity = %q %q %q, preview=%+v", gotWorkspace, gotThread, gotAuthor, got)
	}
}

func TestShellPreWriteExpandedExecutionPaths(t *testing.T) {
	registry := pluginProxyTestFixture.get(t, testToolPluginDeclaration)
	proxy, _ := newShellStorageTestProxy(t)
	proxy.registry = registry
	proxy.commentary = newCommentaryBroker()
	server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	t.Cleanup(server.Close)
	proxy.commentaryEndpoint = server.URL
	if _, err := proxy.storeShellRuntime("prewrite-thread"); err != nil {
		t.Fatal(err)
	}
	proxy.prepareShellCommentary("prewrite-thread", "history", "agent")
	t.Setenv(shellruntime.RuntimeDirectoryEnvironment, proxy.shellDirectory)
	t.Setenv(shellruntime.ThreadIDEnvironment, "prewrite-thread")
	var previews []liveDiffPreview
	proxy.commentary.previewPublisher = func(_, _, _ string, preview liveDiffPreview) { previews = append(previews, preview) }
	dir := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt", "pipe.txt", "redirect.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `
make_patch() { printf touched >> side-effects; printf '%s' 'append "expanded\n"'; }
hpatch one.txt "$(make_patch)"
printf '%s' 'append "piped\n"' | hpatch pipe.txt
printf '%s' 'append "redirected\n"' > patch.in
hpatch redirect.txt < patch.in
mkdir nested
cd nested
printf '%s' 'append "dependent\n"' | hpatch ../two.txt
`
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	shell, ok := registry.contribution("shell")
	if !ok {
		t.Fatal("shell unavailable")
	}
	token := proxy.commentary.subscribe(dir+"\x00session", "call", "agent")
	proxy.commentary.bindActivity(token, "prewrite-thread")
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	execution, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell, []string{"bash", script}, nil, dir, os.Environ(), sink, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	stdout.WriteString(execution.Stdout)
	stderr.WriteString(execution.Stderr)
	if execution.ExitCode != 0 {
		t.Fatalf("shell code=%d stdout=%q stderr=%q", execution.ExitCode, stdout.String(), stderr.String())
	}
	if got, err := os.ReadFile(filepath.Join(dir, "side-effects")); err != nil || string(got) != "touched" {
		t.Fatalf("side effects = %q, %v", got, err)
	}
	for name, want := range map[string]string{"one.txt": "expanded\n", "pipe.txt": "piped\n", "redirect.txt": "redirected\n", "two.txt": "dependent\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", name, got, err)
		}
	}
	if len(previews) != 8 {
		t.Fatalf("publication count = %d: %+v", len(previews), previews)
	}
	for i := 0; i < len(previews); i += 2 {
		if !previews[i].Evaluated || previews[i].Complete || !previews[i+1].Complete || len(previews[i].Files) != 1 || previews[i].Files[0].Diff != previews[i+1].Files[0].Diff {
			t.Fatalf("preview pair %d = %+v / %+v", i/2, previews[i], previews[i+1])
		}
	}
}
