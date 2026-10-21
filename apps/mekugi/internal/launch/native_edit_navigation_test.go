package launch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestNativeEditClickOpensExactCapturedDiffDialog(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, repeated := range []bool{false, true} {
			t.Run(fmt.Sprint(grouped, repeated), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				captures := []presentation.Chunk{
					liveDiffCapture("wrong", "file20.go", 1, "wrong-old\n", "wrong-new\n", presentation.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
					liveDiffCapture("target", "file20.go", 2, "target-old\n", "target-new\n", presentation.Origin{Change: "amber20", Caller: "/root", Source: "apply_patch"}),
				}
				c := liveDiffChangesPane(t, 120, 28, captures)
				terminalTest(u).Auxiliary = c
				wrong := presentation.Attempt{Key: presentation.AttemptKey{Workspace: "/w", Call: "wrong"}, Correlation: "call\x000", Change: "amber1", Operation: activitydetail.Operation{Thread: "child", Item: "call"}, Chunks: captures[:1]}
				attempt := presentation.Attempt{Key: presentation.AttemptKey{Workspace: "/w", Call: "target"}, Correlation: "call\x000", Change: "amber20", Operation: activitydetail.Operation{Thread: "main", Item: "call"}, Chunks: captures[1:]}
				if grouped {
					attempt.Correlation = "exec-carrier"
					attempt.Receipt = &presentation.Receipt{Thread: "main", Calls: []string{"call"}}
				}
				u.review.Projection.Data = reviewFixtureSnapshot(t, wrong, attempt)
				publishFixtureReview(t, u)

				path := "earlier.go"
				if repeated {
					path = "file20.go"
				}
				u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "Edit `" + path + "` +1 -1 · apply_patch", Native: &sessionpresentation.NativeItem{Thread: "main", Item: "earlier"}}, {Seq: 2, Agent: "Main", Kind: "tool", Text: "Edit `file20.go` +1 -1 · apply_patch", Native: &sessionpresentation.NativeItem{Thread: "main", Item: "call"}}}})
				terminalTest(u).DiffOpen, terminalTest(u).Focus = true, 1
				for _, key := range []byte("a\t/amber1\r") {
					if err := terminalTest(u).key(key); err != nil {
						t.Fatal(err)
					}
				}
				before := strings.Join(paintSavedDiff(t, c, 120, 28), "\n")

				if err := u.testPaint(&bytes.Buffer{}, 120, 28); err != nil {
					t.Fatal(err)
				}
				if !terminalTest(u).openActivityEdit(u.shell.Transcript, 2, "file20.go") || terminalTest(u).Dialog == nil {
					t.Fatal("edit did not open dialog")
				}
				if !terminalTest(u).DiffOpen || terminalTest(u).Focus != 1 || strings.Join(paintSavedDiff(t, c, 120, 28), "\n") != before {
					t.Fatal("edit dialog changed live diff navigation")
				}
				if err := u.testPaint(&bytes.Buffer{}, 120, 28); err != nil {
					t.Fatal(err)
				}
				page := terminalTest(u).Dialog.Document.Text
				if !strings.Contains(page, "target-new") || strings.Contains(page, "wrong-new") {
					t.Fatalf("wrong invocation diff: %q", page)
				}
				terminalTest(u).outputKey("\x1b")
				if terminalTest(u).Dialog != nil || strings.Join(paintSavedDiff(t, c, 120, 28), "\n") != before {
					t.Fatal("closing edit dialog changed prior diff state")
				}
			})
		}
	}
}

func TestNativeCompletedBatchEditOpensBeforeSiblingCommandExits(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint(child), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			thread, view := "main", u.testActivity(u.shell.Transcript)
			if child {
				thread, view = "child", u.testActivity(u.shell.Agents)
				registerThread(&u.attached.session, binding.ThreadInfo{ID: thread, AgentNickname: "worker"})
				_ = u.Requests.ReadThreadMetadata("main", thread, true, appServerRequest{})
				u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/worker")
			}
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "batch", "status": "inProgress"}})
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "batch", "item": map[string]any{"id": "slow-sibling", "type": "commandExecution", "command": "sleep 30", "status": "inProgress"}})
			change := map[string]any{"path": "batch.go", "kind": map[string]any{"type": "update"}, "diff": "@@ -1 +1 @@\n-before_batch\n+after_batch\n"}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "batch", "item": map[string]any{"id": "patch", "type": "fileChange", "status": "completed", "changes": []any{change}}})
			finishPacing(u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents))
			screen := vt.NewEmulator(160, 40)
			defer screen.Close()
			if err := u.testPaint(screen, 160, 40); err != nil {
				t.Fatal(err)
			}
			if u.review.Projection.Data.Len() != 0 || u.attached.session.Commands()[[3]string{thread, "batch", "slow-sibling"}] == nil {
				t.Fatal("fixture must have no durable capture and a running batch sibling")
			}
			// Click the actual rendered edit, through the shared pointer routing.
			x, y := -1, -1
			for row := range 40 {
				for col := range 160 - len("batch.go") {
					var text strings.Builder
					for offset := range len("batch.go") {
						text.WriteString(screen.CellAt(col+offset, row).Content)
					}
					if text.String() == "batch.go" {
						x, y = col, row
						break
					}
				}
				if x >= 0 {
					break
				}
			}
			if x < 0 {
				t.Fatalf("edit row missing:\n%s", screen.String())
			}
			for _, end := range []string{"M", "m"} {
				if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", x+1, y+1, end)); err != nil {
					t.Fatal(err)
				}
			}
			if terminalTest(u).Dialog == nil {
				t.Fatal("completed nested edit was unresponsive before batch exit")
			}
			if err := u.testPaint(screen, 160, 40); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(terminalTest(u).Dialog.Document.Text, "+after_batch") || terminalTest(u).Dialog.View != view.View {
				t.Fatalf("wrong edit opened:\n%s", screen.String())
			}
			if u.attached.session.Commands()[[3]string{thread, "batch", "slow-sibling"}] == nil {
				t.Fatal("opening a diff altered host command lifecycle")
			}
		})
	}
}

func TestNativeHostEditNavigationRequiresCompletedSuccessfulItem(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.activity = fixtureActivityWithoutDetails(t)
	for _, tc := range []struct{ phase, status string }{{"item/started", ""}, {"item/completed", "failed"}, {"item/completed", "declined"}, {"item/completed", "inProgress"}} {
		item := binding.ThreadItem{Type: "fileChange", Status: tc.status, Changes: []binding.FileChange{{Path: "a.go", Diff: "+not_confirmed\n"}}}
		u.attached.source.Entries = []sessionpresentation.ActivityRecord{{Source: sessionpresentation.ActivityEntry{Seq: 1, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "patch", EditPages: activitypresentation.HostEditPages(item.Observation(), u.attached.cwd, tc.phase)}}}}
		if terminalTest(u).openActivityEdit(u.shell.Transcript, 1, "a.go") || terminalTest(u).Dialog != nil {
			t.Fatalf("%s/%s opened a completed edit", tc.phase, tc.status)
		}
	}
	item := binding.ThreadItem{Changes: []binding.FileChange{{Path: "first.go", Diff: "+first\n"}, {Path: "second.go", Diff: "+second\n"}}}
	u.attached.source.Entries = []sessionpresentation.ActivityRecord{{Source: sessionpresentation.ActivityEntry{Seq: 2, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "patch", EditPages: activitypresentation.HostEditPages(item.Observation(), u.attached.cwd, "item/completed")}}}}
	if terminalTest(u).openActivityEdit(u.shell.Transcript, 2, "missing.go") || !terminalTest(u).openActivityEdit(u.shell.Transcript, 2, "second.go") || terminalTest(u).Dialog.Page != 1 {
		t.Fatal("host diff did not resolve the exact clicked path")
	}
}

func TestUISnapshotNativeCompletedHostEditDialog(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := binding.ThreadItem{Changes: []binding.FileChange{{Path: "internal/router/batch.go", Diff: "@@ -1 +1 @@\n-before_batch\n+after_batch\n"}}}
	u.attached.source.Entries = []sessionpresentation.ActivityRecord{{Source: sessionpresentation.ActivityEntry{Seq: 1, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "patch", EditPages: activitypresentation.HostEditPages(item.Observation(), u.attached.cwd, "item/completed")}}}}
	if !terminalTest(u).openActivityEdit(u.shell.Transcript, 1, "internal/router/batch.go") {
		t.Fatal("host edit did not open")
	}
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	uisnapshot.Assert(t, "testdata/snapshots/native-completed-host-edit-dialog.txt", drawOutputDialog(terminalTest(u))+"\n")
}

func TestNativeEditDialogShowsHistoricalHunkNotLaterCapture(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	before := liveDiffLinesFile("target", 100)
	after := strings.Replace(before, "target_line_80", "clicked_hunk", 1)
	later := strings.Repeat("later_hunk\n", 100) + after
	captures := []presentation.Chunk{
		liveDiffCapture("clicked", "target.go", 1, before, after, presentation.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
		liveDiffCapture("later", "target.go", 2, after, later, presentation.Origin{Change: "amber2", Caller: "/root", Source: "apply_patch"}),
	}
	c := liveDiffChangesPane(t, 120, 24, captures)
	terminalTest(u).Auxiliary = c
	u.review.Projection.Data = reviewFixtureSnapshot(t, presentation.Attempt{Key: presentation.AttemptKey{Workspace: "/w", Call: "clicked"}, Correlation: "call\x000", Change: "amber1", Operation: activitydetail.Operation{Thread: "main", Item: "call"}, Chunks: captures[:1]})
	publishFixtureReview(t, u)

	u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "Edit `target.go` +1 -1 · apply_patch", Native: &sessionpresentation.NativeItem{Thread: "main", Item: "call"}}}})
	if err := os.WriteFile(filepath.Join(u.attached.cwd, "target.go"), []byte("unrelated_workspace_content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !terminalTest(u).openActivityEdit(u.shell.Transcript, 1, "target.go") {
		t.Fatal("edit dialog failed")
	}
	if err := u.testPaint(&bytes.Buffer{}, 120, 24); err != nil {
		t.Fatal(err)
	}
	if got := terminalTest(u).Dialog.Document.Text; !strings.Contains(got, "clicked_hunk") || !strings.Contains(got, "target_line_70") || !strings.Contains(got, "target_line_90") || strings.Contains(got, "later_hunk") || strings.Contains(got, "unrelated_workspace_content") {
		t.Fatalf("dialog did not show historical capture: %q", got)
	}
	terminalTest(u).outputKey("\x1b")
	paintSavedDiff(t, c, 120, 24)
	if !strings.Contains(strings.Join(c.Selection(119, 23).Document, "\n"), "later_hunk") {
		t.Fatal("closing dialog lost later capture")
	}
}

func TestNativeCapturedDetailIsAttachmentScoped(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	chunk := liveDiffCapture("edit", "scope.go", 1, "before\n", "after\n", presentation.Origin{})
	u.review.Projection.Data = reviewFixtureSnapshot(t, presentation.Attempt{
		Key:       presentation.AttemptKey{Workspace: "/w", Call: "edit"},
		Operation: activitydetail.Operation{Thread: "main", Item: "edit"}, Chunks: []presentation.Chunk{chunk},
	})
	publishFixtureReview(t, u)
	u.attached.source.Entries = []sessionpresentation.ActivityRecord{{Source: sessionpresentation.ActivityEntry{Seq: 1, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "edit"}}}}
	// Retained reads are unavailable after publication. Detail navigation must
	// consume the accepted snapshot without reopening evidence.
	u.review.Projection.Changes = nil
	u.review.Projection.Retained.Store = testHistory().Attach(filepath.Join(t.TempDir(), "unavailable"))
	if !terminalTest(u).openActivityEdit(u.shell.Transcript, 1, "scope.go") || !strings.Contains(terminalTest(u).Dialog.Pages[0].Code, "+after") {
		t.Fatal("accepted capture did not resolve without retained storage")
	}
	terminalTest(u).outputKey("\x1b")
	u.attached.session.Retire()
	source := u.attached.source
	u.attached = newAttachedPresentation(u.presentationState, u.conversationState, u.observations)
	u.attached.source = source // Keep the old target to test scope admission, not absence.
	if terminalTest(u).openActivityEdit(u.shell.Transcript, 1, "scope.go") || terminalTest(u).Dialog != nil {
		t.Fatal("new attachment inherited the prior captured-detail publication")
	}
}
