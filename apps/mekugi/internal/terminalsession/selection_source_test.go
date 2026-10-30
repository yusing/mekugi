package terminalsession

import (
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestCommonPathSelectionCopiesOriginalOperands(t *testing.T) {
	for _, command := range []string{"cat", "rg needle", "ls", "rg --files"} {
		source := command + " src/one.go src/two.go"
		var blocks []activityui.Block
		for _, content := range feed.ParseOperations(activitypresentation.ShellSource(source, "/workspace")) {
			blocks = append(blocks, activityui.Block{Content: content})
		}
		blocks = activityui.MergeTargets(activityui.GroupOperations(blocks))
		blocks[0].Duration, blocks[0].Workdir, blocks[0].ShowWorkdir = time.Second, "/workspace", true
		blocks[0].Approval, blocks[0].Repeated = "Approved", "again"
		p := activityui.Painter{
			CopySource: true}
		rows := p.Block(blocks[0], 100)
		copyRows := make([][]terminalui.CopySpan, len(rows))
		for i := range rows {
			rows[i], copyRows[i] = terminalui.ExtractCopy(rows[i])
		}
		selection := NewSelection(Surface{Rect: Rect{W: 100, H: len(rows)}, Rows: rows, CopySource: copyRows}, 0, 0)
		selection.Move(99, len(rows)-1, true)
		got := selection.Text()
		if !strings.Contains(got, "src/one.go") || !strings.Contains(got, "src/two.go") || strings.ContainsAny(got, "{}") || command == "rg needle" && !strings.Contains(got, "needle") {
			t.Errorf("%s copied %q", source, got)
		}
		for _, suffix := range []string{"/workspace", "1s", "Approved", "again"} {
			if !strings.Contains(got, suffix) {
				t.Errorf("%s copy lost %q: %q", source, suffix, got)
			}
			plain := ansi.Strip(rows[0])
			column := ansi.StringWidth(plain[:strings.Index(plain, suffix)])
			selection := NewSelection(Surface{Rect: Rect{W: 100, H: len(rows)}, Rows: rows, CopySource: copyRows}, column, 0)
			selection.Move(column+len(suffix)-1, 0, true)
			if got := selection.Text(); got != suffix {
				t.Errorf("%s suffix copied %q, want %q", source, got, suffix)
			}
		}
	}
}

func TestComposerSelectionOffscreenSecretRemainsMasked(t *testing.T) {
	var main Main
	view := activityui.NewView()
	text := strings.Repeat("secret\n", 19) + "secret"
	painted := main.layout(mainInput{Draft: composer.Draft{Text: text}, Secret: true}, &view.Painter, 40, 12, 0)
	var frame Frame
	if err := frame.Write(io.Discard, RenderedFrame{Rows: painted.Rows, composer: painted.composer}, 40, false); err != nil {
		t.Fatal(err)
	}
	in := FrameInput{Main: &main, Transcript: view, Shell: &Shell{Layout: Layout{Main: Rect{W: 40, H: 12}}}, Width: 40, Height: 12}
	r := main.ComposerRect
	result := frame.SelectionMouse(in, nil, Mouse{X: r.X + 5, Y: r.Y + r.H - 1})
	if result.Selection == nil || result.Selection.ScreenRows() == 0 {
		t.Fatal("secret composer did not retain masked offscreen rows")
	}
	s := result.Selection
	s.ScrollKey("\x1b[H")
	s.Mouse(0, r.X, r.Y, true)
	if got, want := s.Text(), strings.ReplaceAll(text, "secret", "••••••"); got != want {
		t.Fatalf("secret selection copied %q, want masked rows", got)
	}
}

func TestFrozenSelectionRequestsUpdatedOutput(t *testing.T) {
	var main Main
	var prompts Prompts
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	main.Update(sessionpresentation.View{Main: thread, Displayed: thread, Version: sessionpresentation.Version{Attachment: 1}})
	view := activityui.NewView()
	view.Expansion = 2
	publication := activitypresentation.Publication{Entries: []activitypresentation.Entry{{
		Seq: 7, Agent: "Main", Kind: "tool", Text: "command", Outputs: map[int]int{1: 2},
		Values: []activitypresentation.Value{{Kind: "op", Verb: "Run", Label: "printf retained", Source: 7, Output: 1, Exited: true, Tail: []string{"tail"}, TailOmitted: 1}},
	}, {
		Seq: 8, Agent: "Main", Kind: "text", Text: strings.Repeat("recent line\n", 12),
		Values: []activitypresentation.Value{{Kind: "text", Body: strings.Repeat("recent line\n", 12), Source: 8}},
	}}}
	view.Update(new(publication), activityui.Display{Caller: "/root"})
	painted := main.Render(view, &view.Painter, &prompts, MainOptions{Now: time.Unix(100, 0)}, 60, 8, 0)
	for _, demand := range painted.Demands {
		view.Hydrate([]activitypresentation.OutputResult{{Demand: demand, View: feed.OutputView{Lines: []string{"old retained line", "tail"}}}})
	}
	painted = main.Render(view, &view.Painter, &prompts, MainOptions{Now: time.Unix(100, 0)}, 60, 8, 0)
	if len(painted.Demands) != 0 || view.FeedLines <= view.FeedRows {
		t.Fatalf("fixture did not leave older output offscreen: demands=%v lines=%d rows=%d", painted.Demands, view.FeedLines, view.FeedRows)
	}
	var frame Frame
	if err := frame.Write(io.Discard, RenderedFrame{Rows: painted.Rows}, 60, false); err != nil {
		t.Fatal(err)
	}
	// Source publication can advance between delivery and the next pointer event.
	publication.Entries = slices.Clone(publication.Entries)
	publication.Entries[0].Revision++
	publication.Entries[0].Outputs = map[int]int{1: 3}
	view.Update(new(publication), activityui.Display{Caller: "/root"})
	in := FrameInput{Main: &main, Transcript: view, Shell: &Shell{Layout: Layout{Main: Rect{0, 0, 60, 8}}}, Width: 60, Height: 8}
	mouse := Mouse{X: view.FeedLeft - 1, Y: view.FeedTop - 1}
	result := frame.SelectionMouse(in, nil, mouse)
	if !result.Handled || len(result.Demands) == 0 {
		t.Fatal("frozen capture did not request its offscreen output")
	}
	results := make([]activitypresentation.OutputResult, len(result.Demands))
	for i, demand := range result.Demands {
		if demand.Source != 7 || demand.Output != 1 || demand.Version != 3 {
			t.Fatalf("wrong retained target: %+v", demand)
		}
		results[i] = activitypresentation.OutputResult{Demand: demand, View: feed.OutputView{Lines: []string{"retained first line", "tail"}}}
	}
	// Asynchronous acquisition retains the original feed even if the live
	// publication advances and invalidates the demanded output in that view.
	publication.Entries = slices.Clone(publication.Entries)
	publication.Entries[0].Revision++
	publication.Entries[0].Outputs = map[int]int{1: 4}
	publication.Entries[1].Values = []activitypresentation.Value{{Kind: "text", Body: "replacement feed", Source: 8}}
	view.Update(new(publication), activityui.Display{Caller: "/root"})
	if !result.Selection.CompleteCapture(results) || !strings.Contains(strings.Join(result.Selection.Rows, "\n"), "retained first line") || strings.Contains(strings.Join(result.Selection.Rows, "\n"), "replacement feed") {
		t.Fatalf("frozen capture lost hydrated content: %+v", result)
	}
}

func TestSelectionTargetRetainsPressedIdentity(t *testing.T) {
	var main Main
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	v := sessionpresentation.View{Main: thread, Displayed: thread, Info: sessionpresentation.MainInfo{Workspace: "/original"}, Version: sessionpresentation.Version{Attachment: 7, Revision: 1}}
	main.Update(v)
	view := activityui.NewView()
	view.FeedLeft, view.FeedTop, view.FeedRight, view.FeedRows = 1, 1, 20, 1
	view.FeedQuestions = []uint64{11}
	var frame Frame
	if err := frame.Write(io.Discard, RenderedFrame{Rows: []string{"question", ""}, Source: SourceContext{Attachment: 7, Thread: "main", Workspace: "/original"}}, 20, false); err != nil {
		t.Fatal(err)
	}
	in := FrameInput{Main: &main, Transcript: view, Shell: &Shell{Layout: Layout{Main: Rect{0, 0, 20, 1}}}, Width: 20, Height: 2}
	v.Version = sessionpresentation.Version{Attachment: 8, Revision: 5}
	v.Info.Workspace = "/replacement"
	main.Update(v)
	pressed := frame.SelectionMouse(in, nil, Mouse{X: 1})
	if pressed.Selection == nil {
		t.Fatal("press did not retain its target")
	}
	view.FeedQuestions = []uint64{22}
	released := frame.SelectionMouse(in, pressed.Selection, Mouse{X: 1, Release: true})
	if !released.Handled || released.Selection != nil || released.Target.Version != (sessionpresentation.Version{Attachment: 7, Revision: 5}) || released.Target.Entry != 11 || released.Target.Workspace != "/original" || released.Target.Thread != "main" {
		t.Fatalf("press retargeted: %+v", released)
	}
}
