package router

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotNativeResetRecoveryDisclosure(t *testing.T) {
	for _, mode := range []string{"live", "restored", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			proxy.journalCompaction = "auto"
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{
				{Op: "add", Kind: "context", Title: new("Preserve host authority"), Body: new("Use retained observations, not revived continuation handles.")},
				{Op: "add", Kind: "task", Title: new("Continue validation")},
			}); err != nil {
				t.Fatal(err)
			}
			response, expected := deliverRecoveryCompaction(t, proxy, workspace, thread, "reset-turn")
			u := newAppServerSessionTestUI(t, workspace)
			u.thread, u.proxy = thread, proxy
			u.session.start(thread, workspace)
			u.journal = &nativeJournalSink{workspace: workspace, thread: thread}
			u.view.painter.Theme = livediff.DarkTheme
			item := appServerItem{ID: "reset-item", Type: "contextCompaction"}
			if mode != "live" {
				proxy.replayStore.bindStandaloneCompactionItem(t.Context(), workspace, thread, "reset-turn", item.ID)
				if mode == "unavailable" {
					if err := os.Remove(filepath.Join(proxy.replayStore.directory, journalCompactionRecoveryName(workspace, thread, response))); err != nil {
						t.Fatal(err)
					}
				}
				reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(reopened.snapshots.close)
				u.proxy = &mekugiProxy{replayStore: reopened}
				u.restoreHistory([]appServerHistoryTurn{{ID: "reset-turn", Status: "completed", Items: []appServerItem{item}}})
			} else {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "reset-turn", "item": item})
			}
			// A later journal mutation must not change the text opened by this row.
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "log", Text: new("Newer fact must not replace historical recovery")}}); err != nil {
				t.Fatal(err)
			}
			for _, width := range []int{44, 90} {
				t.Run(fmt.Sprint(width), func(t *testing.T) {
					feed := u.view.renderFeed(width, 20)
					row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool { return s != (liveActivitySnippet{}) })
					if row < 0 {
						t.Fatal("reset commentary has no click target")
					}
					assertNativeUISnapshot(t, fmt.Sprintf("reset-recovery-row-%d", width), feed.lines)
					u.view.snippet = feed.snippets[row]
					hovered := u.view.renderFeed(width, 20)
					if !strings.Contains(hovered.lines[row], "\x1b[4m") {
						t.Fatal("reset disclosure lacks hover affordance")
					}
					shell := selectionTestUI(feed.lines...)
					shell.main = u
					u.view.feedTop, u.view.feedLeft, u.view.feedRight, u.view.feedRows = 1, 1, width, len(feed.lines)
					u.view.feedSnippets = feed.snippets
					if !shell.selectionMouse(0, 2, row, false) || !shell.selectionMouse(0, 2, row, true) || shell.output == nil {
						t.Fatal("mouse click did not open recovery dialog")
					}
					page := u.view.painter.DialogPage(shell.output.pages[0], width)
					if mode == "unavailable" {
						if !strings.Contains(page.Text, "unavailable") || strings.Contains(page.Text, "Newer fact") {
							t.Fatalf("missing evidence was substituted: %s", page.Text)
						}
					} else if page.Text != expected {
						t.Fatalf("dialog is not exact model-visible recovery: %q != %q", page.Text, expected)
					}
					rows := make([]string, 18)
					shell.paintOutput(rows, width, len(rows))
					snapshotMode := "retained"
					if mode == "unavailable" {
						snapshotMode = mode
					}
					assertNativeUISnapshot(t, fmt.Sprintf("reset-recovery-dialog-%s-%d", snapshotMode, width), rows)
					before := u.view.offset
					shell.outputKey("G")
					shell.paintOutput(rows, width, len(rows))
					if mode != "unavailable" && shell.output.top == 0 {
						t.Fatal("long recovery message did not scroll")
					}
					if u.view.offset != before {
						t.Fatal("dialog scrolling moved transcript")
					}
					shell.outputKey("q")
					if shell.output != nil {
						t.Fatal("dialog did not close")
					}
					if got := ansi.Strip(strings.Join(u.view.renderFeed(width, 20).lines, "\n")); strings.Contains(got, "Preserve host authority") {
						t.Fatal("closing dialog expanded recovery into transcript")
					}
				})
			}
		})
	}
}

func TestNativeProviderCompactionHasNoRecoveryDisclosure(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": u.thread, "turnId": "provider-turn", "item": appServerItem{ID: "provider-item", Type: "contextCompaction"}})
	feed := u.view.renderFeed(80, 20)
	if slices.ContainsFunc(feed.snippets, func(s liveActivitySnippet) bool { return s != (liveActivitySnippet{}) }) {
		t.Fatal("provider compaction gained a journal recovery target")
	}
	if u.progressRecovery("Context reset from journal", "foreign", "reset-turn", "reset-item") != "" {
		t.Fatal("recovery crossed active thread")
	}
}
