package launch

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func awaitMarkdownLinks(t *testing.T, u *terminalUI) {
	t.Helper()
	for {
		if err := u.paintNative(t.Context(), io.Discard); err != nil {
			t.Fatal(err)
		}
		if len(u.main.links.pending) == 0 {
			return
		}
		select {
		case result := <-u.main.links.results:
			u.main.links.finish(result)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for link admission")
		}
	}
}

func TestMarkdownLinksVisibleDemandAndReuse(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"first.go", "last.go"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("package sample\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	main := newAppServerSessionTestUI(t, workspace)
	main.testActivity(main.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "Main", Kind: "final", Text: "first.go\n\n" + strings.Repeat("ordinary prose\n\n", 40) + "last.go and missing.go", Observed: time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)},
	}})
	u := terminalTest(main)
	u.Width, u.Height = 80, 18
	if err := main.testPaint(io.Discard, u.Width, u.Height); err != nil {
		t.Fatal(err)
	}
	if len(main.links.pending) == 0 {
		t.Fatalf("visible paths did not request checks: %q", strings.Join(u.Frame.Rows, "\n"))
	}
	for key := range main.links.pending {
		if key.target == "first.go" {
			t.Fatal("off-screen path requested a check")
		}
	}
	// Withhold admission, as with a delayed filesystem completion. Input and
	// rendering must remain usable before any result is delivered.
	appServerTestKeys(t, main, "still typing")
	if main.Text != "still typing" {
		t.Fatal("pending checks blocked composer input")
	}
	awaitMarkdownLinks(t, u)
	scope := u.frameInput().SourceContext()
	known := main.links.known(scope)
	if !known["last.go"] || known["missing.go"] {
		t.Fatalf("existence admission = %v", known)
	}
	if _, checked := known["first.go"]; checked {
		t.Fatal("hidden path was checked")
	}
	main.shell.Transcript.ScrollKey('g')
	awaitMarkdownLinks(t, u)
	if !known["first.go"] {
		t.Fatal("scrolling did not admit newly visible path")
	}
	if err := os.Remove(filepath.Join(workspace, "last.go")); err != nil {
		t.Fatal(err)
	}
	main.shell.Transcript.ScrollKey('G')
	awaitMarkdownLinks(t, u)
	if !known["last.go"] || len(main.links.pending) != 0 {
		t.Fatal("scroll-back discarded a completed check")
	}
	if !strings.Contains(strings.Join(u.Frame.Rows, "\n"), "]8;;last.go") {
		t.Fatal("scroll-back lost the admitted hyperlink")
	}
}

func TestMarkdownLinkCheckScopeAndLocations(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source #1.go")
	if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	scope := terminalsession.SourceContext{Attachment: 1, Thread: "main", Workspace: workspace}
	other := terminalsession.SourceContext{Attachment: 1, Thread: "child", Workspace: t.TempDir()}
	var checks markdownLinkChecks
	targets := []string{"source #1.go", "source #1.go:2-3", fileURL + ":2", "missing.go"}
	checks.request(t.Context(), scope, targets)
	for range targets {
		select {
		case result := <-checks.results:
			if len(checks.known(other)) != 0 {
				t.Fatal("old completion leaked into replacement scope")
			}
			checks.finish(result)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for stat")
		}
	}
	for _, target := range targets[:3] {
		if !checks.known(scope)[target] {
			t.Fatalf("existing destination %q not admitted", target)
		}
	}
	checks.request(t.Context(), other, []string{targets[0]})
	checks.finish(<-checks.results)
	if checks.known(other)[targets[0]] || !checks.known(scope)[targets[0]] {
		t.Fatal("workspace checks were shared across scopes")
	}
	checks.request(t.Context(), scope, targets)
	if len(checks.pending) != 0 {
		t.Fatal("positive or negative results were probed again")
	}
	checks.request(t.Context(), scope, []string{"late.go"})
	checks.selectAttachment(2)
	select {
	case result := <-checks.results:
		checks.finish(result)
	case <-time.After(5 * time.Second):
		t.Fatal("retired check did not complete")
	}
	if len(checks.admitted) != 0 || len(checks.pending) != 0 {
		t.Fatal("retired completion recreated old admissions or retained its slot")
	}
}

func TestMarkdownLinksClippedAndHiddenDialog(t *testing.T) {
	main := newAppServerSessionTestUI(t, t.TempDir())
	view := main.testActivity(main.shell.Transcript)
	view.appendEntry(sessionpresentation.ActivityEntry{Seq: 1, Agent: "Main", Kind: "reasoning", Observed: time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)}, []activityui.Block{{Kind: "summary", Label: strings.Repeat("ordinary ", 20) + "hidden.go", Body: strings.Repeat("body paragraph\n\n", 10), Collapsed: true}})
	view.publishActivity()
	view.Disclosure.Collapse(1, 0)
	u := terminalTest(main)
	u.Width, u.Height = 80, 18
	if err := main.testPaint(io.Discard, u.Width, u.Height); err != nil {
		t.Fatal(err)
	}
	for _, row := range u.Frame.Rows {
		if strings.Contains(ansi.Strip(row), "hidden.go") {
			t.Fatal("fixture exposed the clipped target")
		}
	}
	if len(main.links.pending) != 0 {
		t.Fatal("clipped-out candidate requested a check")
	}
	u.openBlocks(main.shell.Transcript, []activityui.Block{{Body: "a.go b.go c.go d.go e.go f.go"}})
	if err := u.paintNative(t.Context(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(main.links.pending) != 4 {
		t.Fatalf("visible dialog did not fill bounded capacity: %v", main.links.pending)
	}
	u.Width, u.Height = 8, 5
	awaitMarkdownLinks(t, u)
	known := main.links.known(u.frameInput().SourceContext())
	if len(known) != 4 {
		t.Fatalf("invisible dialog retained stale demands: %v", known)
	}
}

func TestMarkdownLinksChildAndDialog(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "child.go"), []byte("package child"), 0600); err != nil {
		t.Fatal(err)
	}
	main := newAppServerSessionTestUI(t, workspace)
	child := main.testActivity(main.shell.Agents)
	child.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "/root/child", Kind: "final", Text: "child.go and missing.go"}}})
	child.displayThread("/root/child")
	u := terminalTest(main)
	u.Width, u.Height = 80, 18
	if err := main.testPaint(io.Discard, u.Width, u.Height); err != nil {
		t.Fatal(err)
	}
	awaitMarkdownLinks(t, u)
	if scope := u.frameInput().SourceContext(); scope.Thread == "main" || !main.links.known(scope)["child.go"] {
		t.Fatalf("child admission used the wrong scope: %+v, %+v", scope, main.links.admitted)
	}
	if !strings.Contains(strings.Join(u.Frame.Rows, "\n"), "]8;;child.go") {
		t.Fatal("child transcript omitted the existing link")
	}
	u.openBlocks(main.shell.Agents, []activityui.Block{{Body: "child.go and missing.go"}})
	awaitMarkdownLinks(t, u)
	rows := strings.Join(u.Frame.Rows, "\n")
	if strings.Contains(rows, "mekugi-file-candidate:") || strings.Contains(rows, "]8;;missing.go") {
		t.Fatal("dialog leaked an unchecked or absent link")
	}
	x, y := outputSelectionPoint(t, u, "child.go")
	u.outputMouse(0, x, y, false)
	u.outputMouse(0, x, y, true)
	awaitMarkdownFile(t, u)
	if u.Dialog.File.Path != "child.go" {
		t.Fatal("child dialog link did not open its admitted file")
	}
}

func TestMarkdownLinksReplay(t *testing.T) {
	source := replayPlaybackTestSource()
	source.Cwd = t.TempDir()
	if err := os.WriteFile(filepath.Join(source.Cwd, "replay.go"), []byte("package replay"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range source.Events {
		if source.Events[i].Item.ID == "answer" {
			source.Events[i].Item.Text = "replay.go and missing.go"
		}
	}
	p := newTestUIReplayPlayback(nil, t.Context(), source, 1)
	t.Cleanup(p.close)
	for cycle := range 3 {
		if cycle > 0 {
			if err := p.seek(0); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.advance(20 * time.Second); err != nil {
			t.Fatal(err)
		}
		for {
			replayPlaybackTestPaint(t, p, 80, 18)
			if len(p.ui.links.pending) == 0 {
				break
			}
			select {
			case result := <-p.ui.links.results:
				p.ui.links.finish(result)
			case <-time.After(5 * time.Second):
				t.Fatal("replay link checks did not finish")
			}
		}
		if len(p.ui.links.admitted) != 1 {
			t.Fatal("replay retained retired attachment admissions")
		}
	}
	rows := strings.Join(p.ui.shell.Frame.Rows, "\n")
	if !strings.Contains(rows, "]8;;replay.go") || strings.Contains(rows, "]8;;missing.go") || strings.Contains(rows, "mekugi-file-candidate:") {
		t.Fatalf("replay admission was not delivered: %q", rows)
	}
}
