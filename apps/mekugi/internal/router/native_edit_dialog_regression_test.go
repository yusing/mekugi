package router

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

type editDialogRegressionCase struct {
	kind, verb, before, after, host string
}

func editDialogRegressionCases() []editDialogRegressionCase {
	return []editDialogRegressionCase{
		{kind: "add", verb: "Create", after: "var answer = 42", host: "var answer = 42"},
		{kind: "delete", verb: "Delete", before: "var answer = 41\n", host: "var answer = 41\n"},
		{kind: "update", verb: "Edit", before: "var answer = 41\n", after: "var answer = 42\n", host: "@@ -1 +1 @@\n-var answer = 41\n+var answer = 42\n"},
	}
}

func openRegressionEditDialog(t *testing.T, tc editDialogRegressionCase, captured bool) (*terminalUI, string) {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.Painter.Theme = diffrender.DarkTheme
	const path = "answer.go"
	want := tc.host
	if captured {
		beforePath, afterPath := "/w/"+path, "/w/"+path
		if tc.kind == "add" {
			beforePath = ""
		}
		if tc.kind == "delete" {
			afterPath = ""
		}
		chunk := presentation.Chunk{Key: "edit", CaptureOrder: 1, Review: diffmodel.RenderReviewFile(beforePath, afterPath, tc.before, tc.after)}
		c := liveDiffChangesPane(t, 120, 24, []presentation.Chunk{chunk})
		u.shell.Auxiliary = c
		u.review.Projection.Data = reviewFixtureSnapshot(t, presentation.Attempt{AttemptIdentity: presentation.AttemptIdentity{Key: presentation.AttemptKey{Workspace: "/w", Call: "patch"}, Correlation: "patch\x000"}, Operation: activitydetail.Operation{Thread: "main", Item: "patch"}, Chunks: []presentation.Chunk{chunk}})
		publishFixtureReview(t, u)
		want = chunk.Review.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(c.Workspace(), chunk.Review.BeforePath), pathdisplay.ForWorkspace(c.Workspace(), chunk.Review.AfterPath))
		u.source.Entries = []sessionpresentation.ActivityRecord{{Source: sessionpresentation.ActivityEntry{Seq: 1, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "patch"}}}}
	} else {
		change := binding.FileChange{Path: path, Diff: tc.host}
		change.Kind.Type = tc.kind
		item := binding.ThreadItem{Type: "fileChange", Status: "completed", Changes: []binding.FileChange{change}}
		u.source.Entries = []sessionpresentation.ActivityRecord{{Source: sessionpresentation.ActivityEntry{Seq: 1, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "patch", EditPages: activitypresentation.HostEditPages(item.Observation(), u.cwd, "item/completed")}}}}
	}
	if !u.shell.openActivityEdit(u.view, 1, path) {
		t.Fatal("edit dialog did not open")
	}
	return u.shell, want
}

func TestNativeEditDialogRegressionActionsCopyAndSyntax(t *testing.T) {
	for _, captured := range []bool{false, true} {
		for _, tc := range editDialogRegressionCases() {
			t.Run(fmt.Sprintf("captured=%t/%s", captured, tc.kind), func(t *testing.T) {
				u, wantText := openRegressionEditDialog(t, tc, captured)
				drawOutputDialog(u)
				block := u.output.pages[0]
				if block.Verb != tc.verb {
					t.Fatalf("verb = %q, want %q", block.Verb, tc.verb)
				}
				if block.Code != wantText || u.output.Document.Text != wantText {
					t.Fatalf("plain bytes changed: code %q, copy %q, want %q", block.Code, u.output.Document.Text, wantText)
				}
				u.outputKey("y")
				if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(wantText)) + "\x07"; u.frame.Clipboard != want {
					t.Fatalf("clipboard bytes changed: %q", u.frame.Clipboard)
				}
				var renderer diffrender.Renderer
				for _, side := range []struct {
					source string
					kind   byte
				}{{tc.before, '-'}, {tc.after, '+'}} {
					if side.source == "" {
						continue
					}
					colored, err := renderer.ColorSource(t.Context(), diffrender.DarkTheme, "answer.go", strings.TrimSuffix(side.source, "\n")+"\n")
					if err != nil {
						t.Fatal(err)
					}
					wantLine := colored[0]
					if tc.kind == "update" {
						before, after, err := renderer.ColorHunk(t.Context(), diffrender.DarkTheme,
							diffmodel.ReviewFile{BeforePath: "answer.go", AfterPath: "answer.go"},
							[]diffmodel.ReviewRow{{Kind: '-', Text: tc.before}, {Kind: '+', Text: tc.after}})
						if err != nil {
							t.Fatal(err)
						}
						wantLine = after[0]
						if side.kind == '-' {
							wantLine = before[0]
						}
					}
					if captured || tc.kind == "update" {
						wantLine = diffrender.SourceLine(diffrender.DarkTheme, ansi.StringWidth(wantLine)+4, "", wantLine, side.kind)
					}
					found := false
					for _, line := range u.output.Document.Lines {
						if line.Text == wantLine {
							found = true
						}
					}
					if !found {
						t.Fatalf("dialog missing exact pane token colors for %q: want %q, lines %+v", side.source, wantLine, u.output.Document.Lines)
					}
				}
			})
		}
	}
}

func TestUISnapshotNativeEditDialogRegression(t *testing.T) {
	for _, captured := range []bool{false, true} {
		for _, tc := range editDialogRegressionCases() {
			origin := "host"
			if captured {
				origin = "captured"
			}
			t.Run(origin+"/"+tc.kind, func(t *testing.T) {
				u, _ := openRegressionEditDialog(t, tc, captured)
				uisnapshot.Assert(t, "testdata/snapshots/native-edit-dialog-"+origin+"-"+tc.kind+".txt", drawOutputDialog(u)+"\n")
			})
		}
	}
}

func TestUISnapshotNativeEditDialogSurroundingContext(t *testing.T) {
	before := liveDiffLinesFile("historical", 50)
	for _, tc := range []struct {
		name string
		line int
	}{{"start", 0}, {"middle", 24}, {"end", 49}} {
		t.Run(tc.name, func(t *testing.T) {
			after := strings.Replace(before, fmt.Sprintf("historical_line_%02d", tc.line), "changed_line", 1)
			u, _ := openRegressionEditDialog(t, editDialogRegressionCase{kind: "update", before: before, after: after}, true)
			rows := make([]string, 36)
			u.paintOutput(rows, 100, len(rows))
			uisnapshot.Assert(t, "testdata/snapshots/native-edit-dialog-context-"+tc.name+".txt", strings.Join(rows, "\n")+"\n")
		})
	}
}
