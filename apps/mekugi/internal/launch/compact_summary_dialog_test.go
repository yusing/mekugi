package launch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotCompactionSummaryDialog(t *testing.T) {
	const summary = "## Current work\n\nKeep the **first** boundary's exact summary.\n\n```go\nreturn saved\n```\n"
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	owner := executionobservation.New(executionobservation.Config{Retained: p.retained})
	first, err := owner.StoreCompact(t.Context(), "main", summary, `{"sources":[{"path":"never-read.txt"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := owner.StoreCompact(t.Context(), "main", "A different later summary.", "")
	if err != nil {
		t.Fatal(err)
	}
	// A local fork can retain copied ancestor metadata before its own boundaries.
	records := []map[string]any{replayTestMeta("main"), replayTestMeta("ancestor")}
	for i, ref := range []string{first, second} {
		turn, item := []string{"one", "two"}[i], []string{"first", "second"}[i]
		records = append(records,
			replayTestRecord("event_msg", replayTestEpoch+int64(i)*3000, map[string]any{"type": "task_started", "turn_id": turn}),
			replayTestRecord("compacted", replayTestEpoch+int64(i)*3000+500, map[string]any{"replacement_history": []any{map[string]any{"type": "compaction", "encrypted_content": ref}}}),
			replayTestItem("main", turn, replayTestEpoch+int64(i)*3000+100, replayTestEpoch+int64(i)*3000+1000, map[string]any{"id": item, "type": "ContextCompaction"}),
			replayTestRecord("event_msg", replayTestEpoch+int64(i)*3000+1500, map[string]any{"type": "task_complete", "turn_id": turn}),
		)
	}
	path := replayTestWrite(t, t.TempDir(), "rollout-main.jsonl", records...)
	for _, mode := range []string{"live", "restore", "replay"} {
		t.Run(mode, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.retainedSession = history.Session{Store: p.retained.Store, Thread: "main", Context: t.Context()}
			u.RolloutPath = path
			u.attached.compaction = func(thread, item string) func(context.Context) (string, error) {
				return compactionSummaryReader(u.retainedSession, u.RolloutPath, thread, item)
			}
			if mode == "live" {
				for i, id := range []string{"first", "second"} {
					appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": []string{"one", "two"}[i], "item": binding.ThreadItem{ID: id, Type: "contextCompaction"}})
				}
			} else if mode == "restore" {
				info := binding.ThreadInfo{ID: "main", Path: path, Cwd: u.attached.cwd, Turns: []binding.HistoryTurn{
					{ID: "one", Status: "completed", Items: []binding.ThreadItem{{ID: "first", Type: "contextCompaction"}}},
					{ID: "two", Status: "completed", Items: []binding.ThreadItem{{ID: "second", Type: "contextCompaction"}}},
				}}
				if err := u.restoreContent(info); err != nil {
					t.Fatal(err)
				}
			} else {
				source, err := replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: p.retained.Store.Directory(), Seed: 1})
				if err != nil {
					t.Fatal(err)
				}
				replay := newTestUIReplayPlayback(u.activity, t.Context(), source, 1)
				defer replay.close()
				if err := replay.seek(replay.Until); err != nil {
					t.Fatal(err)
				}
				replay.publish()
				feed := replay.ui.view.RenderFeed(80, 20)
				for _, snippet := range feed.Snippets {
					block, ok := replay.ui.view.SnippetBlock(snippet)
					if !ok || block.Kind != "compaction" || block.Body != summary {
						continue
					}
					view := replay.ui.presentation.View()
					target := terminalsession.SelectionTarget{Version: view.Version, Thread: view.Displayed.ID, View: replay.ui.view, Snippet: snippet}
					effects := slices.Values([]terminalsession.InputEffect{{Selection: &terminalsession.SelectionInput{Target: target}}})
					if err := replay.applyInput(effects); err != nil {
						t.Fatal(err)
					}
					if replay.ui.shell.Dialog == nil {
						t.Fatal("replay click did not open the shared dialog")
					}
					replay.ui.shell.Dialog.Paint(make([]string, 20), 80, 20)
					if got := replay.ui.shell.Dialog.Document.Text; got != summary {
						t.Fatalf("replay summary=%q", got)
					}
					return
				}
				t.Fatalf("replay summary has no clickable target: %q", feed.Lines)
			}
			_, view, _ := nativeDetailTestViews(t, u)
			publishActivity(&view.attached, view.shell)
			feed := view.shell.Transcript.RenderFeed(80, 20)
			for _, snippet := range feed.Snippets {
				block, ok := view.shell.Transcript.SnippetBlock(snippet)
				if !ok || block.Kind != "compaction" || block.Source != view.attached.source.Entries[0].Source.Seq {
					continue
				}
				if failure := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: block.Source}); failure != "" {
					t.Fatal(failure)
				}
				if mode == "live" {
					result := <-view.files.fileReads
					view.files.loadedMarkdownFile(&view.attached.session, view.attached.presentation, view.attached.cwd, view.shell, result)
				}
				rows := make([]string, 20)
				view.shell.Dialog.Paint(rows, 80, 20)
				if got := view.shell.Dialog.Document.Text; got != summary {
					t.Fatalf("%s summary=%q", mode, got)
				}
				if mode == "live" {
					uisnapshot.AssertTerminal(t, "testdata/snapshots/compaction-summary-dialog.txt", append(rows, "plain after dialog"), 80)
				}
				// An open dialog keeps its selected summary after feed aging.
				view.attached.source.Entries = nil
				view.files.prepareOutput(&view.attached.session, view.shell)
				view.shell.Dialog.Paint(make([]string, 20), 80, 20)
				if view.shell.Dialog.Document.Text != summary {
					t.Fatal("feed aging changed the open summary")
				}
				return
			}
			t.Fatalf("summary has no clickable target: %q", feed.Lines)
		})
	}
	foreign, err := owner.StoreCompact(t.Context(), "foreign", "foreign summary", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := compact.ReadSummary(p.retained.Store, "main", foreign); err == nil {
		t.Fatal("foreign summary was admitted")
	}
}

func TestCompactionSummaryReadLeavesInputResponsiveAndRejectsStaleResult(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	u.attached.compaction = func(thread, item string) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			close(started)
			defer close(done)
			select {
			case <-release:
			case <-time.After(time.Second):
			}
			return "late summary", nil
		}
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "one", "item": binding.ThreadItem{ID: "boundary", Type: "contextCompaction"}})
	_, view, _ := nativeDetailTestViews(t, u)
	seq := view.attached.source.Entries[0].Source.Seq
	if failure := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeEntryDetails, Source: seq}); failure != "" {
		t.Fatal(failure)
	}
	<-started
	select {
	case <-done:
		t.Fatal("dialog opening waited for storage")
	default:
	}
	u.applyComposerKey("x")
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "responsive"}})
	if u.Turn != "responsive" || u.attached.conversation.Editor.Text != "x" {
		t.Fatal("input/lifecycle did not proceed while storage was blocked")
	}
	old := view.files.fileReads
	view.openNoticeDetails("replacement dialog")
	close(release)
	result := <-old
	if changed, _ := view.files.loadedMarkdownFile(&view.attached.session, view.attached.presentation, view.attached.cwd, view.shell, result); changed {
		t.Fatal("late summary replaced another dialog")
	}
	view.shell.Dialog.Paint(make([]string, 20), 80, 20)
	if view.shell.Dialog.Document.Text != "replacement dialog" {
		t.Fatal("replacement dialog changed")
	}
}

func TestCompactionReplayChargesRetainedSummaryBytes(t *testing.T) {
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	owner := executionobservation.New(executionobservation.Config{Retained: p.retained})
	ref, err := owner.StoreCompact(t.Context(), "main", strings.Repeat("x", 16<<20), "")
	if err != nil {
		t.Fatal(err)
	}
	records := []map[string]any{replayTestMeta("main")}
	for i := range 17 {
		records = append(records,
			replayTestRecord("compacted", replayTestEpoch+int64(i)*1000, map[string]any{"replacement_history": []any{map[string]any{"type": "compaction", "encrypted_content": ref}}}),
			replayTestItem("main", "turn", replayTestEpoch+int64(i)*1000, replayTestEpoch+int64(i)*1000+1, map[string]any{"type": "ContextCompaction", "id": fmt.Sprint(i)}),
		)
	}
	path := replayTestWrite(t, t.TempDir(), "main.jsonl", records...)
	_, err = replayTestService(t, nil).Read(t.Context(), playback.ReadOptions{Path: path, HistoryDirectory: p.retained.Store.Directory()})
	if err == nil || !strings.Contains(err.Error(), "replay corpus exceeds 256 MiB") {
		t.Fatalf("retained summary budget: %v", err)
	}
}
