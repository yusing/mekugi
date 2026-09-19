package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubagentHpatchCommittedActivity(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	existing := filepath.Join(directory, "existing.txt")
	if err := os.WriteFile(existing, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const root, child = "activity-root", "activity-child"
	activity := newSubagentActivity()
	if !activity.observe(root, "", "/root", false) || !activity.observe(child, root, "/root/worker", true) {
		t.Fatal("failed to establish root and child activity relationship")
	}

	var publishedCall string
	broker := newCommentaryBroker()
	broker.editPublisher = func(ctx context.Context, workspace, thread, callID string) error {
		publishedCall = callID
		return store.publishEditReceipt(ctx, workspace, thread, callID, activity)
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	sink := &httpShellCommentarySink{
		endpoint: server.URL,
		token:    broker.subscribeThread(directory+"\x00child", child, "/root/worker"),
		client:   server.Client(),
	}
	shell, _ := registry.contribution("shell")
	if err := os.WriteFile(filepath.Join(directory, "added.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := `hpatch existing.txt 'type "old" "new"' added.txt 'append "first\nsecond\n"'`
	result, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", script}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+child), sink, nil, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("execution=%+v err=%v", result, err)
	}
	if publishedCall == "" {
		t.Fatal("successful edit did not publish its committed receipt")
	}

	messages := activity.drain(root, time.Time{}, maxCommentaryPublicationBytes)
	if len(messages) != 1 {
		t.Fatalf("activity messages=%d", len(messages))
	}
	text := commentaryText(t, messages[0])
	if !strings.Contains(text, "Edit ") {
		t.Fatalf("activity missing Edit label: %q", text)
	}
	for _, want := range []string{
		commentaryCode(existing) + " +1 -1",
		commentaryCode(filepath.Join(directory, "added.txt")) + " +2 -0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("activity %q missing %q", text, want)
		}
	}
	if strings.Contains(text, `type "old"`) || strings.Contains(text, "first\\nsecond") {
		t.Fatalf("activity exposed patch source: %q", text)
	}

	if err := store.publishEditReceipt(t.Context(), directory, child, publishedCall, activity); err != nil {
		t.Fatal(err)
	}
	if got := activity.drain(root, time.Time{}, maxCommentaryPublicationBytes); len(got) != 0 {
		t.Fatalf("duplicate receipt emitted %d messages", len(got))
	}
	if err := store.publishEditReceipt(t.Context(), directory, "wrong-thread", publishedCall, activity); err == nil {
		t.Fatal("wrong thread published receipt")
	}
	if got := activity.drain(root, time.Time{}, maxCommentaryPublicationBytes); len(got) != 0 {
		t.Fatalf("wrong-thread receipt emitted %d messages", len(got))
	}

	rejected, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", `hpatch existing.txt 'type "missing" "bad"'`}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+child), sink, nil, nil)
	if err != nil || rejected.ExitCode == 0 {
		t.Fatalf("rejected execution=%+v err=%v", rejected, err)
	}
	if got := activity.drain(root, time.Time{}, maxCommentaryPublicationBytes); len(got) != 0 {
		t.Fatalf("rejected edit emitted %d success messages", len(got))
	}
	_, recoveryTail, ok := strings.Cut(rejected.Stderr, "Recover this rejected edit with hpatch --recover ")
	if !ok {
		t.Fatal(rejected.Stderr)
	}
	handle, _, _ := strings.Cut(recoveryTail, ".")
	failed, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch --recover " + handle + ` 'type "other" "new"'`}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+child), sink, nil, nil)
	if err != nil || failed.ExitCode == 0 {
		t.Fatalf("failed recovery execution=%+v err=%v", failed, err)
	}
	if got := activity.drain(root, time.Time{}, maxCommentaryPublicationBytes); len(got) != 0 {
		t.Fatalf("failed recovery emitted %d success messages", len(got))
	}

	fixed, err := executeShellTool(t.Context(), manifest, registry.RuntimeRoot, &shell,
		[]string{"bash", "hpatch --recover " + handle + ` 'type "missing" "new"'`}, nil, directory,
		append(os.Environ(), "CODEX_THREAD_ID="+child), sink, nil, nil)
	if err != nil || fixed.ExitCode != 0 {
		t.Fatalf("successful recovery execution=%+v err=%v", fixed, err)
	}
	recovered := activity.drain(root, time.Time{}, maxCommentaryPublicationBytes)
	if len(recovered) != 1 {
		t.Fatalf("successful recovery messages=%d", len(recovered))
	}
	recoveryText := commentaryText(t, recovered[0])
	if !strings.Contains(recoveryText, "Edit "+commentaryCode(existing)+" +1 -1") ||
		strings.Contains(recoveryText, "--recover") || strings.Contains(recoveryText, handle) {
		t.Fatalf("successful recovery activity=%q", recoveryText)
	}
}
