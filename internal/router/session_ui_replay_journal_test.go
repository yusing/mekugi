package router

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func replayJournalTestEvidence(t *testing.T) (*mekugiReplayStore, string, string, mekugiHistory) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	proxy.commentaryEndpoint = "http://127.0.0.1:1234/internal/commentary"
	const source = `await journal({op:"add", title:"Checked the implementation"});`
	lowered, changed, err := transform.lowerCodeModeCommentary("replay-journal-cell", source)
	if err != nil || !changed {
		t.Fatalf("lowering: changed=%v error=%v", changed, err)
	}
	command := workerCommand("mjournal", []string{commentaryOnceArgument, proxy.commentaryEndpoint, transform.commentarySubscriptions[0].token}) + journalTransportArgument(t, map[string]any{"op": "add", "title": "Checked the implementation"})
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ToolName: "exec", Script: source, CarrierPayload: lowered, ExecutingThread: "root"}
	return store, workspace, command, history
}

func replayJournalTestSource(t *testing.T) *sessionUIReplay {
	t.Helper()
	store, workspace, command, history := replayJournalTestEvidence(t)
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"replay-journal-cell": history}); err != nil {
		t.Fatal(err)
	}
	prefix, _, _ := strings.Cut(command, " '")
	read := prefix + journalTransportArgument(t, map[string]any{"op": "read", "p": "/1"})
	// Offline ingestion must not acquire a lock, rewrite records or chmod the
	// directory. Even the normal writer's lock is absent before the read.
	if err := os.Remove(filepath.Join(store.directory, "store.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.directory, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store.directory, 0700) })
	before, err := os.Stat(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	meta := replayTestMeta("root")
	meta["payload"].(map[string]any)["cwd"] = workspace
	epoch := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local).UnixMilli()
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", meta,
		replayTestRecord("event_msg", epoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", epoch+100, epoch+500, map[string]any{"id": "transport", "type": "CommandExecution", "command": []string{"bash", "-lc", execsegment.ShScript("/private/exec-track.sh", command)}, "aggregated_output": `{"ok":true,"items":[]}`, "status": "completed", "exit_code": 0}),
		replayTestItem("root", "turn", epoch+520, epoch+580, map[string]any{"id": "journal-read", "type": "CommandExecution", "command": []string{"bash", "-lc", execsegment.ShScript("/private/exec-track.sh", read)}, "aggregated_output": `{"ok":true,"items":[]}`, "status": "completed", "exit_code": 0}),
		replayTestItem("root", "turn", epoch+600, epoch+1000, map[string]any{"id": "journal-message", "type": "AgentMessage", "text": "Journal: Checked the implementation."}),
		replayTestItem("root", "turn", epoch+1100, epoch+1500, map[string]any{"id": "ordinary-command", "type": "CommandExecution", "command": []string{"sh", "-c", "mjournal --help"}, "aggregated_output": "Journal command help", "status": "completed", "exit_code": 0}),
		replayTestRecord("event_msg", epoch+2000, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	r, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		t.Fatal("offline ingestion modified the replay directory")
	}
	if _, err := os.Lstat(filepath.Join(store.directory, "store.lock")); !os.IsNotExist(err) {
		t.Fatalf("offline ingestion created a store lock: %v", err)
	}
	if r.Items != 3 || r.JournalUnverified != 0 {
		t.Fatalf("replay metadata: items=%d unverified=%d", r.Items, r.JournalUnverified)
	}
	for _, e := range r.Events {
		if e.Params.ItemID == "transport" {
			t.Fatalf("transport or synthetic output survived ingestion: %+v", e)
		}
	}
	return r
}

func TestSessionUIReplayJournalPreservesSemanticMessagesAndSeek(t *testing.T) {
	r := replayJournalTestSource(t)
	p := newUIReplayPlayback(t.Context(), r, 1)
	t.Cleanup(p.close)
	for _, position := range []time.Duration{2 * time.Second, 0, 2 * time.Second} {
		if err := p.seek(position); err != nil {
			t.Fatal(err)
		}
		if position == 0 {
			if len(p.ui.view.entries) != 0 {
				t.Fatal("backward seek retained future journal content")
			}
			continue
		}
		var text strings.Builder
		for _, entry := range p.ui.view.entries {
			text.WriteString(entry.Text)
		}
		if !strings.Contains(text.String(), "Checked the implementation") || !strings.Contains(text.String(), "Read `journal /1`") || !strings.Contains(text.String(), "mjournal --help") || strings.Contains(text.String(), "--journal-once") {
			t.Fatalf("lost meaningful journal or ordinary command: %s", text.String())
		}
	}
}

func TestUISnapshotSessionUIReplayJournal(t *testing.T) {
	p := newUIReplayPlayback(t.Context(), replayJournalTestSource(t), 1)
	t.Cleanup(p.close)
	p.ui.view.painter.Theme, p.ui.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
	if err := p.seek(p.until); err != nil {
		t.Fatal(err)
	}
	p.paused = true
	uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-journal.txt", replayPlaybackTestPaint(t, p, 120, 28))
}

func TestSessionUIReplayJournalRequiresExactProvenance(t *testing.T) {
	for _, tc := range []string{"verified", "read", "tracked", "paged", "empty-workspace", "foreign-thread", "foreign-workspace", "untranslated", "changed-endpoint", "compound", "tracked compound", "ordinary", "missing", "corrupt"} {
		t.Run(tc, func(t *testing.T) {
			store, workspace, command, history := replayJournalTestEvidence(t)
			scope := workspace
			wantHidden, wantCandidate := false, true
			var wantActions []appServerCommandAction
			switch tc {
			case "verified":
				wantHidden = true
			case "read":
				prefix, _, _ := strings.Cut(command, " '")
				command = prefix + journalTransportArgument(t, map[string]any{"op": "read", "agent": "worker"})
				wantCandidate, wantActions = false, []appServerCommandAction{{Type: "read", Path: "journal worker"}}
			case "tracked":
				command = execsegment.ShScript("/private/exec-track.sh", command)
				wantHidden = true
			case "paged":
				command += " 2 " + strings.Repeat("a", 64)
				wantHidden = true
			case "empty-workspace":
				scope, wantHidden = "", true
			case "foreign-thread":
				history.ExecutingThread = "other"
			case "foreign-workspace":
				scope = t.TempDir()
			case "untranslated":
				history.CarrierPayload = history.Script
			case "changed-endpoint":
				command = strings.Replace(command, "1234", "5678", 1)
			case "compound":
				command += "; echo visible"
				wantCandidate = false
			case "tracked compound":
				command = execsegment.ShScript("/private/exec-track.sh", command+"; echo visible")
				wantCandidate = false
			case "ordinary":
				command = "mjournal --help"
				wantCandidate = false
			}
			if tc != "missing" {
				if err := store.put(t.Context(), scope, map[string]mekugiHistory{"replay-journal-cell": history}); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "corrupt" {
				if err := os.WriteFile(filepath.Join(store.directory, replayRecordName(scope, "replay-journal-cell", false)), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// A verified transport is no longer a candidate: it is hidden or typed.
			wantUnverified := wantCandidate && !wantHidden
			shown, hidden, unverified := replayJournalTransport(store, workspace, "root", appServerItem{Type: "commandExecution", Command: command})
			if hidden != wantHidden || unverified != wantUnverified || !slices.Equal(shown.CommandActions, wantActions) || shown.Command != command {
				t.Fatalf("hidden=%v unverified=%v actions=%+v want=%v/%v/%+v", hidden, unverified, shown.CommandActions, wantHidden, wantUnverified, wantActions)
			}
		})
	}
}

func TestSessionUIReplayJournalMissingStoreStaysReadOnly(t *testing.T) {
	store, workspace, command, _ := replayJournalTestEvidence(t)
	store.directory = filepath.Join(t.TempDir(), "absent-store")
	_, hidden, unverified := replayJournalTransport(store, workspace, "root", appServerItem{Type: "commandExecution", Command: command})
	if hidden || !unverified {
		t.Fatalf("missing provenance: hidden=%v unverified=%v", hidden, unverified)
	}
	if _, err := os.Stat(store.directory); !os.IsNotExist(err) {
		t.Fatalf("offline replay created missing store: %v", err)
	}
}

func TestSessionUIReplayJournalTransportPreservesRecordedInterval(t *testing.T) {
	store, workspace, command, history := replayJournalTestEvidence(t)
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"replay-journal-cell": history}); err != nil {
		t.Fatal(err)
	}
	meta := replayTestMeta("root")
	meta["payload"].(map[string]any)["cwd"] = workspace
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", meta,
		replayTestItem("root", "turn", replayTestEpoch, replayTestEpoch+2000, map[string]any{"id": "transport", "type": "CommandExecution", "command": []string{"sh", "-c", command}}))
	r, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.End.Sub(r.Start) != 2*time.Second || len(r.Events) != 2 || r.Items != 0 {
		t.Fatalf("hidden transport changed recorded interval: %+v", r)
	}
	p := newUIReplayPlayback(t.Context(), r, 1)
	t.Cleanup(p.close)
	if err := p.seek(p.until); err != nil {
		t.Fatal(err)
	}
	if len(p.ui.view.entries) != 0 || p.position != 2*time.Second {
		t.Fatal("hidden transport created presentation or shortened playback")
	}
}
