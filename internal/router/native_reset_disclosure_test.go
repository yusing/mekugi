package router

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotNativeResetRecoveryDisclosure(t *testing.T) {
	for _, mode := range []string{"live", "restored", "unavailable", "slice-live", "slice-restored", "slice-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			var driver *journalResetDriver
			if strings.HasPrefix(mode, "slice-") {
				driver, _ = resetDriverFixture(t, "slice")
				proxy, workspace, thread = driver.proxy, driver.workspace, driver.thread
				if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, thread, func(j *threadJournal, _ bool) error {
					for i := range j.Items {
						for _, stamp := range []*journalStamp{j.Items[i].Started, j.Items[i].Finished} {
							if stamp != nil {
								stamp.At = "2026-09-30T12:00:00Z"
							}
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := driver.tick(driver.deadline); err != nil {
					t.Fatal(err)
				}
				driver.compactTurn = "reset-turn"
			}
			proxy.journalCompaction = "auto"
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{
				{Op: "add", Kind: "context", Title: new("Preserve host authority"), Body: new("Use retained observations, not revived continuation handles.")},
				{Op: "add", Kind: "task", Title: new("Continue validation")},
			}); err != nil {
				t.Fatal(err)
			}
			response, expected := deliverRecoveryCompaction(t, proxy, workspace, thread, "reset-turn")
			if driver != nil {
				if err := driver.continuePlan(true); err != nil {
					t.Fatal(err)
				}
			}
			u := newAppServerSessionTestUI(t, workspace)
			u.thread, u.proxy = thread, proxy
			u.reset = driver
			u.session.start(thread, workspace)
			u.journal = &nativeJournalSink{workspace: workspace, thread: thread}
			if driver != nil {
				u.journal = proxy.journals.attachNative(workspace, thread)
				t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
				if err := proxy.journals.restoreNative(t.Context(), proxy.replayStore, u.journal); err != nil {
					t.Fatal(err)
				}
			}
			u.view.painter.Theme = livediff.DarkTheme
			u.view.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
			u.clock = u.view.clock
			item := appServerItem{ID: "reset-item", Type: "contextCompaction"}
			if mode != "live" && mode != "slice-live" {
				proxy.replayStore.bindStandaloneCompactionItem(t.Context(), workspace, thread, "reset-turn", item.ID)
				if strings.HasSuffix(mode, "unavailable") {
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
				u.reset = nil
				u.restoreHistory([]appServerHistoryTurn{{ID: "reset-turn", Status: "completed", Items: []appServerItem{item}}})
			} else {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "reset-turn", "item": item})
			}
			if driver != nil {
				applyResetWordingJournal(u)
			}
			fixResetPresentationTime(u)
			if got := strings.Count(ansi.Strip(strings.Join(u.view.renderFeed(90, 20).lines, "\n")), "Context reset from journal"); got != 1 {
				t.Fatalf("expected one journal reset, got %d", got)
			}
			for _, entry := range u.view.entries {
				if entry.Kind == "progress" {
					t.Fatal("journal reset leaked into commentary")
				}
			}
			// A later journal mutation must not change the text opened by this row.
			if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "log", Text: new("Newer fact must not replace historical recovery")}}); err != nil {
				t.Fatal(err)
			}
			for _, width := range []int{44, 90} {
				t.Run(fmt.Sprint(width), func(t *testing.T) {
					feed := u.view.renderFeed(width, 20)
					row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool {
						block, ok := u.view.snippetBlock(s)
						return ok && block.Verb == "Journal recovery"
					})
					if row < 0 {
						t.Fatal("reset journal row has no click target")
					}
					rowName := "reset-recovery-row"
					if mode == "slice-live" {
						rowName += "-slice-live"
					}
					assertNativeUISnapshot(t, fmt.Sprintf("%s-%d", rowName, width), feed.lines)
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
					if strings.HasSuffix(mode, "unavailable") {
						if !strings.Contains(page.Text, "unavailable") || strings.Contains(page.Text, "Newer fact") {
							t.Fatalf("missing evidence was substituted: %s", page.Text)
						}
					} else if page.Text != expected {
						t.Fatalf("dialog is not exact model-visible recovery: %q != %q", page.Text, expected)
					}
					rows := make([]string, 18)
					shell.paintOutput(rows, width, len(rows))
					snapshotMode := "retained"
					if strings.HasSuffix(mode, "unavailable") {
						snapshotMode = "unavailable"
					} else if driver != nil {
						snapshotMode = "slice-retained"
					}
					assertNativeUISnapshot(t, fmt.Sprintf("reset-recovery-dialog-%s-%d", snapshotMode, width), rows)
					before := u.view.offset
					shell.outputKey("G")
					shell.paintOutput(rows, width, len(rows))
					if !strings.HasSuffix(mode, "unavailable") && shell.output.top == 0 {
						t.Fatal("long recovery message did not scroll")
					}
					if u.view.offset != before {
						t.Fatal("dialog scrolling moved transcript")
					}
					shell.outputKey("q")
					if shell.output != nil {
						t.Fatal("dialog did not close")
					}
					if got := ansi.Strip(strings.Join(u.view.renderFeed(width, 20).lines, "\n")); got != ansi.Strip(strings.Join(feed.lines, "\n")) {
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

func TestNativeResetRecoveryReplayKeepsJournalClickTarget(t *testing.T) {
	d, _ := resetDriverFixture(t, "slice")
	if err := d.tick(d.deadline); err != nil {
		t.Fatal(err)
	}
	d.compactTurn = "reset-turn"
	_, expected := deliverRecoveryCompaction(t, d.proxy, d.workspace, d.thread, d.compactTurn)
	if err := d.continuePlan(true); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(d.proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.snapshots.close)
	u := newAppServerSessionTestUI(t, d.workspace)
	u.thread = d.thread
	u.proxy = &mekugiProxy{replayStore: reopened}
	u.session.start(d.thread, d.workspace)
	store := newJournalStore()
	u.journal = store.attachNative(d.workspace, d.thread)
	if err := store.restoreNative(t.Context(), reopened, u.journal); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	u.restoreHistory([]appServerHistoryTurn{{ID: d.compactTurn, Status: "completed", StartedAt: now - 1, CompletedAt: now + 1,
		Items: []appServerItem{{ID: "reset-item", Type: "contextCompaction"}}}})
	// Pending delivery after replay must update the same durable reset row,
	// not create a second disclosure or a native commentary row.
	u.applyPendingJournal()
	feed := u.view.renderFeed(90, 30)
	if strings.Count(ansi.Strip(strings.Join(feed.lines, "\n")), "Context reset from journal") != 1 {
		t.Fatal("replayed reset was missing or duplicated")
	}
	for _, snippet := range feed.snippets {
		if block, ok := u.view.snippetBlock(snippet); ok && block.Verb == "Journal recovery" {
			if block.Body != expected {
				t.Fatal("replayed reset lost exact original recovery")
			}
			return
		}
	}
	t.Fatal("replayed journal reset has no recovery disclosure")
}

func TestNativeResetJournalDisclosureKeepsWorkspaceOwner(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Workspace recovery")},
	}); err != nil {
		t.Fatal(err)
	}
	_, expected := deliverRecoveryCompaction(t, proxy, workspace, thread, "reset-turn")
	u := newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = thread, proxy
	u.journal = &nativeJournalSink{workspace: workspace, thread: thread}
	u.unscopedJournal = &nativeJournalSink{thread: thread}
	publication := nativeJournalPublication{event: &journalEvent{Op: "reset", ResetTurn: "reset-turn",
		Fields: journalNode{Kind: "note", Title: "Reset"}}, item: journalItem{ID: "reset-note", Text: "Reset"}}
	u.applyJournalPublication(u.journal, publication)
	u.applyJournalPublication(u.unscopedJournal, publication)
	if u.view.entries[0].native.recovery != expected || u.view.entries[1].native.recovery != journalRecoveryUnavailable {
		t.Fatal("reset disclosure borrowed another journal namespace's recovery")
	}
}

func TestNativeResetDisclosureSurvivesRejectedContinuation(t *testing.T) {
	for _, next := range []string{"pending", "dropped", "removed"} {
		for _, status := range []string{"completed", "interrupted"} {
			t.Run(next+"/"+status, func(t *testing.T) {
				d, wire := resetDriverFixture(t, "slice")
				if err := d.tick(d.deadline); err != nil {
					t.Fatal(err)
				}
				resetDriverReply(t, d, "{}")
				u := newAppServerSessionTestUI(t, d.workspace)
				u.thread, u.proxy, u.reset = d.thread, d.proxy, d
				u.session.start(d.thread, d.workspace)
				u.journal = d.proxy.journals.attachNative(d.workspace, d.thread)
				t.Cleanup(func() { d.proxy.journals.detachNative(u.journal) })
				if err := d.proxy.journals.restoreNative(t.Context(), d.proxy.replayStore, u.journal); err != nil {
					t.Fatal(err)
				}
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": d.thread, "turn": map[string]any{"id": "reset-turn"}})
				_, expected := deliverRecoveryCompaction(t, d.proxy, d.workspace, d.thread, "reset-turn")
				if next != "pending" {
					mutation := journalMutation{Op: "remove", P: "/2"}
					if next == "dropped" {
						mutation = journalMutation{Op: "set", P: "/2", State: new("dropped"), Reason: new("No longer needed")}
					}
					if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{mutation}); err != nil {
						t.Fatal(err)
					}
				}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": d.thread, "turnId": "reset-turn", "item": appServerItem{ID: "reset-item", Type: "contextCompaction"}})
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": d.thread, "turn": map[string]any{"id": "reset-turn", "status": status}})
				u.applyPendingJournal()
				if next == "pending" && status == "completed" {
					resetDriverRequireMethods(t, wire, "thread/compact/start", "turn/start")
				} else {
					resetDriverRequireMethods(t, wire, "thread/compact/start")
				}
				feed := u.view.renderFeed(90, 30)
				if strings.Count(ansi.Strip(strings.Join(feed.lines, "\n")), "Context reset from journal") != 1 {
					t.Fatal("completed reset disappeared or duplicated when continuation settled")
				}
				for _, snippet := range feed.snippets {
					if block, ok := u.view.snippetBlock(snippet); ok && block.Verb == "Journal recovery" && block.Body == expected {
						return
					}
				}
				t.Fatal("completed reset lost exact recovery disclosure")
			})
		}
	}
}
