package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func runtimePreviewFixture(t *testing.T, baseline string) (*appServerUI, string) {
	t.Helper()
	u, _ := runtimeTestUI(t)
	root := t.TempDir()
	u.session.cwd, u.shell.diff.workspace = root, root
	path := filepath.Join(root, "example.txt")
	if err := os.WriteFile(path, []byte(baseline), 0o600); err != nil {
		t.Fatal(err)
	}
	return u, path
}

func runtimePreviewEvent(t *testing.T, u *appServerUI, e session.Event) {
	t.Helper()
	if err := u.runtimeEvent(e); err != nil {
		t.Fatal(err)
	}
}

func runtimeRevealPreview(u *appServerUI) {
	// Advance only the display clock, without sleeping or bypassing event handling.
	u.shell.animating(time.Now().Add(nativeDockReveal))
	u.shell.liveDock.Motion.Enabled = false
	u.shell.diff.previewPane.Motion.Enabled = false
}

func runtimeProposal(t *testing.T, u *appServerUI, id string) diffview.Preview {
	t.Helper()
	runtimeRevealPreview(u)
	dock, pane := u.shell.liveDock.Views[id], u.shell.diff.previewPane.Views[id]
	if dock == nil || pane == nil {
		t.Fatalf("proposal %q missing from shared dock or diff pane", id)
	}
	if dock.Current.ID != id || pane.Current.ID != id || dock.Complete != pane.Complete {
		t.Fatalf("proposal %q has inconsistent dock/pane lifecycle", id)
	}
	return pane.Current
}

func runtimeAssertNoCapture(t *testing.T, u *appServerUI) {
	t.Helper()
	if u.shell.diff.data != nil && len(u.shell.diff.data.attempts) != 0 {
		t.Fatal("native proposal fabricated saved capture evidence")
	}
}

func TestNativeRuntimePreviewProvisionalLifecycle(t *testing.T) {
	const baseline = "first line\nunseen suffix\n"
	u, path := runtimePreviewFixture(t, baseline)
	e := session.Event{Kind: "edit", ID: "write", Role: "Write", Edit: &session.Edit{Path: "example.txt", Content: "incoming\n", Partial: true}}
	runtimePreviewEvent(t, u, e)
	p := runtimeProposal(t, u, e.ID)
	if p.Complete || p.Evaluated || len(p.Files) != 1 {
		t.Fatalf("partial proposal has wrong state: %+v", p)
	}
	for _, line := range strings.Split(p.Files[0].Diff, "\n") {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Fatalf("partial content claims unseen deletion: %q", line)
		}
	}
	if !strings.Contains(p.Files[0].Diff, "+incoming") {
		t.Fatal("incoming prefix not projected")
	}
	e.Edit = &session.Edit{Path: "example.txt", Content: "replacement\n"}
	runtimePreviewEvent(t, u, e)
	p = runtimeProposal(t, u, e.ID)
	if p.Complete || p.Evaluated || !strings.Contains(p.Files[0].Diff, "-unseen suffix") {
		t.Fatalf("final arguments are not an unsettled full proposal: %+v", p)
	}
	runtimeAssertNoCapture(t, u)
	runtimePreviewEvent(t, u, session.Event{Kind: "tool_result", ID: e.ID, Failed: true, Text: "Denied"})
	p = runtimeProposal(t, u, e.ID)
	if !p.Complete || p.Evaluated || !strings.Contains(p.Footer, "not confirmed") {
		t.Fatalf("failed tool result claimed confirmed edit: %+v", p)
	}
	if _, pending := u.runtime.previews[e.ID]; pending {
		t.Fatal("settled preview still pending")
	}
	runtimeAssertNoCapture(t, u)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != baseline {
		t.Fatalf("preview changed baseline: %q, err=%v", got, err)
	}
}

func TestNativeRuntimePreviewEditMatches(t *testing.T) {
	for _, tc := range []struct {
		name, baseline, old     string
		replaceAll, unavailable bool
		want                    string
	}{
		{"unique", "before old after\n", "old", false, false, "+before new after"},
		{"missing", "before other after\n", "old", false, true, ""},
		{"ambiguous", "old and old\n", "old", false, true, ""},
		{"replace all", "old and old\n", "old", true, false, "+new and new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := runtimePreviewFixture(t, tc.baseline)
			runtimePreviewEvent(t, u, session.Event{Kind: "edit", ID: "edit", Role: "Edit", Edit: &session.Edit{Path: "example.txt", Old: tc.old, Content: "new", Replace: true, ReplaceAll: tc.replaceAll}})
			p := runtimeProposal(t, u, "edit")
			if tc.unavailable {
				if !strings.HasPrefix(p.Status, diffview.PreviewUnavailable) || len(p.Files) != 0 {
					t.Fatalf("uncertain match projected a diff: %+v", p)
				}
			} else if len(p.Files) != 1 || !strings.Contains(p.Files[0].Diff, tc.want) {
				t.Fatalf("matching edit = %+v, want %q", p, tc.want)
			}
		})
	}
}

func TestNativeRuntimePreviewIgnoresNonProposalAndHistory(t *testing.T) {
	u, _ := runtimePreviewFixture(t, "baseline\n")
	for _, e := range []session.Event{
		{Kind: "tool", ID: "tool", Role: "Write", Text: `{"file_path":"example.txt","content":"new"}`},
		{Kind: "tool_result", ID: "tool", Role: "Write", Text: "Success"},
		{Kind: "tool", ID: "historical-tool", Role: "Write", Historical: true},
		{Kind: "edit", ID: "historical-edit", Role: "Write", Historical: true, Edit: &session.Edit{Path: "example.txt", Content: "old replay"}},
		{Kind: "edit", ID: "missing-input", Role: "Write"},
	} {
		runtimePreviewEvent(t, u, e)
	}
	if len(u.runtime.previews) != 0 || len(u.shell.livePending) != 0 || len(u.shell.liveDock.Views) != 0 || len(u.shell.diff.previewPane.Views) != 0 {
		t.Fatal("non-proposal or historical event fabricated a live preview")
	}
	runtimeAssertNoCapture(t, u)
}

func TestNativeRuntimePreviewMultipleIDs(t *testing.T) {
	u, _ := runtimePreviewFixture(t, "baseline\n")
	for _, id := range []string{"first", "second"} {
		runtimePreviewEvent(t, u, session.Event{Kind: "edit", ID: id, Role: "Write", Edit: &session.Edit{Path: "example.txt", Content: id + "\n", Partial: true}})
	}
	first, second := runtimeProposal(t, u, "first"), runtimeProposal(t, u, "second")
	if !strings.Contains(first.Files[0].Diff, "+first") || !strings.Contains(second.Files[0].Diff, "+second") {
		t.Fatal("concurrent native IDs borrowed content")
	}
	runtimePreviewEvent(t, u, session.Event{Kind: "tool_result", ID: "first"})
	if !runtimeProposal(t, u, "first").Complete || runtimeProposal(t, u, "second").Complete {
		t.Fatal("settling one native ID changed another's lifecycle")
	}
	if len(u.runtime.previews) != 1 {
		t.Fatal("settling one native ID discarded another")
	}
	runtimeAssertNoCapture(t, u)
}

func TestUISnapshotNativeRuntimePreview(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := runtimePreviewFixture(t, "before\nunseen suffix\n")
			runtimePreviewEvent(t, u, session.Event{Kind: "edit", ID: "write", Role: "Write", Edit: &session.Edit{Path: "example.txt", Content: "incoming preview\n", Partial: true}})
			runtimeRevealPreview(u)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-preview-%d.txt", width)), runtimeFrame(t, u, width, 32))
		})
	}
}

func TestUISnapshotNativeRuntimeMultiSelect(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, client := runtimeTestUI(t)
			question := "Which checks should run?"
			runtimePreviewEvent(t, u, session.Event{Kind: "prompt", Prompt: &session.Prompt{ID: "checks", Tool: "AskUserQuestion", Questions: []session.Question{{Text: question, Header: "Validation", Multiple: true, Options: []session.Option{{Label: "Unit tests", Description: "Fast focused checks"}, {Label: "Snapshots", Description: "Review rendered output"}, {Label: "Integration", Description: "Exercise host boundaries"}}}}}})
			runtimeFrame(t, u, width, 32)
			runtimeKeys(t, u, " \x1b[B \x1b[B")
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-multi-select-%d.txt", width)), runtimeFrame(t, u, width, 32))
			runtimeKeys(t, u, "\r")
			if len(client.decisions) != 1 || !client.decisions[0].Allow || client.decisions[0].ID != "checks" || client.decisions[0].Answers[question] != "Unit tests, Snapshots" {
				t.Fatalf("multi-select decision = %+v", client.decisions)
			}
		})
	}
}
