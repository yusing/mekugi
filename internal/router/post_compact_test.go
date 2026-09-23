package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yusing/mekugi"
)

func TestPostCompactHookRestoresDurableMainThreadStateAfterRestart(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	workspace := t.TempDir()
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	const thread = "root-thread"
	journals := newJournalStore()
	if err := journals.initialize(t.Context(), store, workspace, thread, "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := journals.bindIdentity(t.Context(), store, workspace, thread, "", "/root", true); err != nil {
		t.Fatal(err)
	}
	answerIDs, err := journals.apply(t.Context(), store, workspace, thread, "answer", []journalMutation{{
		Op: "add", Text: new("Use the retained API contract."), Answer: new(true), inferredQuestion: "Which contract should guide the change?",
	}})
	if err != nil {
		t.Fatal(err)
	}
	flushedIDs, err := journals.apply(t.Context(), store, workspace, thread, "flushed", []journalMutation{{
		Op: "add", Text: new("Already delivered, still durable."), ReportNow: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := journals.transaction(t.Context(), store, workspace, thread, func(journal *threadJournal, _ bool) error {
		journal.Items[0].Flushed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sessionCtx, release, err := store.beginSession(t.Context(), thread, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	store = store.scoped(sessionCtx)
	changeID, err := store.reserveChange(t.Context(), workspace, thread, "edit-call")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"edit-call": {
		ChangeID: changeID, CorrelationID: "edit-call", ExecutingThread: thread, Applied: true,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("durable.txt", "durable.txt", "before\n", "after\n")},
	}}); err != nil {
		t.Fatal(err)
	}
	foreignChangeID, err := store.reserveChange(t.Context(), workspace, "sibling-thread", "foreign-edit-call")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"foreign-edit-call": {
		ChangeID: foreignChangeID, CorrelationID: "foreign-edit-call", ExecutingThread: "sibling-thread", Applied: true,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("sibling-private.txt", "sibling-private.txt", "", "private\n")},
	}}); err != nil {
		t.Fatal(err)
	}

	journalPath := filepath.Join(directory, journalFilename(workspace, thread))
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	input := postCompactInput("SessionStart", "compact", thread, workspace)
	var stdout, stderr bytes.Buffer
	if code := RunPostCompactHook(t.Context(), nil, strings.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("hook exit %d: %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected hook diagnostics: %s", stderr.String())
	}
	var response struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode native hook response %q: %v", stdout.String(), err)
	}
	content := response.Output.Context
	for _, want := range []string{
		"SessionStart", "Mekugi post-compaction recovery", answerIDs[0], flushedIDs[0], "Which contract should guide the change?",
		"Use the retained API contract.", "flushed=true", "Already delivered, still durable.",
		"**Changes:**", changeID, "1\t1\tdurable.txt",
	} {
		if want == "SessionStart" {
			if response.Output.Event != want {
				t.Fatalf("hook event = %q, want %q", response.Output.Event, want)
			}
			continue
		}
		if !strings.Contains(content, want) {
			t.Fatalf("recovery context missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, foreignChangeID) || strings.Contains(content, "sibling-private.txt") {
		t.Fatalf("recovery context included another thread's change: %s", content)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !os.SameFile(beforeInfo, afterInfo) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("post-compact read mutated durable journal state")
	}
}

func TestPostCompactHookMainThreadIsolationAndInputFailures(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	const workspace, root, child = "/workspace", "root-thread", "child-thread"
	journals := newJournalStore()
	const sibling = "independent-root-thread"
	for _, item := range []struct{ thread, author, parent string }{
		{root, "/root", ""}, {child, "/root/child", root}, {sibling, "/root", ""},
	} {
		if err := journals.initialize(t.Context(), store, workspace, item.thread, item.author, ""); err != nil {
			t.Fatal(err)
		}
		if err := journals.bindIdentity(t.Context(), store, workspace, item.thread, item.parent, item.author, true); err != nil {
			t.Fatal(err)
		}
		if _, err := journals.apply(t.Context(), store, workspace, item.thread, "seed", []journalMutation{{Op: "add", Text: new("private " + item.thread)}}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("child is skipped", func(t *testing.T) {
		stdout, stderr, code := runPostCompactInput(t, postCompactInput("SessionStart", "compact", child, workspace))
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("child hook = (%d, %q, %q), want clean skip", code, stdout, stderr)
		}
	})
	t.Run("sibling thread state is isolated", func(t *testing.T) {
		stdout, stderr, code := runPostCompactInput(t, postCompactInput("SessionStart", "compact", root, workspace))
		if code != 0 || stderr != "" || !strings.Contains(stdout, "private "+root) || strings.Contains(stdout, "private "+sibling) {
			t.Fatalf("thread-scoped context = (%d, %q, %q), want only %q", code, stdout, stderr, root)
		}
	})
	t.Run("other workspace cannot borrow journal", func(t *testing.T) {
		stdout, stderr, code := runPostCompactInput(t, postCompactInput("SessionStart", "compact", root, "/other-workspace"))
		if code == 0 || stdout != "" || stderr == "" {
			t.Fatalf("cross-workspace lookup = (%d, %q, %q), want advisory failure without context", code, stdout, stderr)
		}
	})
	t.Run("non compact sources skip", func(t *testing.T) {
		for _, event := range []struct{ name, source string }{{"SessionStart", "startup"}, {"SessionStart", "resume"}, {"PostCompact", ""}} {
			stdout, stderr, code := runPostCompactInput(t, postCompactInput(event.name, event.source, root, workspace))
			if code != 0 || stdout != "" || stderr != "" {
				t.Errorf("event %s/%s = (%d, %q, %q), want clean skip", event.name, event.source, code, stdout, stderr)
			}
		}
	})
	t.Run("bad event input is advisory", func(t *testing.T) {
		for _, input := range []string{
			"{", postCompactInput("SessionStart", "compact", "", workspace),
			postCompactInput("SessionStart", "compact", root, "relative/workspace"),
		} {
			stdout, stderr, code := runPostCompactInput(t, input)
			if code == 0 || stdout != "" || stderr == "" {
				t.Errorf("invalid input %q = (%d, %q, %q), want advisory failure without context", input, code, stdout, stderr)
			}
		}
	})
}

func TestPostCompactHookCorruptOrMissingJournalNeverClaimsSuccess(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	const workspace, thread = "/workspace", "root-thread"
	journals := newJournalStore()
	if err := journals.initialize(t.Context(), store, workspace, thread, "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := journals.bindIdentity(t.Context(), store, workspace, thread, "", "/root", true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, journalFilename(workspace, thread))
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runPostCompactInput(t, postCompactInput("SessionStart", "compact", thread, workspace))
	if code == 0 || stdout != "" || stderr == "" {
		t.Fatalf("corrupt journal = (%d, %q, %q), want advisory failure without false recovery", code, stdout, stderr)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runPostCompactInput(t, postCompactInput("SessionStart", "compact", thread, workspace))
	if code == 0 || stdout != "" || stderr == "" {
		t.Fatalf("missing journal = (%d, %q, %q), want advisory failure without false recovery", code, stdout, stderr)
	}
}

func TestBoundCompactSectionIsUTF8BoundedAndProvidesRecoveryPointer(t *testing.T) {
	section := boundCompactSection(strings.Repeat("界", compactSectionBytes), "mchanges --list, then mchanges ID --summary")
	if len(section) > compactSectionBytes {
		t.Fatalf("section has %d bytes, limit is %d", len(section), compactSectionBytes)
	}
	if !utf8.ValidString(section) {
		t.Fatal("bounded section is not valid UTF-8")
	}
	if !strings.Contains(section, "Truncated") || !strings.Contains(section, "mchanges --list") {
		t.Fatalf("truncated section lacks retrieval pointer: %q", section[len(section)-100:])
	}
	if got := boundCompactSection("small", "unused"); got != "small" {
		t.Fatalf("short section changed: %q", got)
	}
}

func postCompactInput(event, source, thread, workspace string) string {
	data, _ := json.Marshal(map[string]string{
		"hook_event_name": event, "source": source, "session_id": thread, "cwd": workspace,
	})
	return string(data)
}

func runPostCompactInput(t *testing.T, input string) (stdout, stderr string, code int) {
	t.Helper()
	var output, diagnostics bytes.Buffer
	code = RunPostCompactHook(t.Context(), nil, strings.NewReader(input), &output, &diagnostics)
	return output.String(), diagnostics.String(), code
}
