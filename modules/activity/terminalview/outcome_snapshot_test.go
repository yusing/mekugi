package terminalview

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// Outcomes share one grammar across operation kinds: failures turn the verb
// red and append `· exit N` or `· failed`; amber stays with live, pending and
// waiting work; unknown exits and no-match searches are neither success nor
// failure.
func TestUISnapshotOperationOutcomes(t *testing.T) {
	theme := diffrender.DarkTheme
	p := Painter{Theme: theme, Clock: func() time.Time { return time.Unix(1005, 0) }}
	var rows []string
	paint := func(blocks ...Block) {
		for _, block := range AlignVerbs(GroupOperations(blocks)) {
			rows = append(rows, p.Block(block, 72)...)
		}
	}
	values := func(item presentation.ItemContent) []Block {
		var blocks []Block
		for _, value := range item.Values() {
			blocks = append(blocks, Block{Value: value})
		}
		return blocks
	}

	// MCP calls: completed, failed and running remain three distinct rows.
	mcp := func(seq uint64, status string, running bool) presentation.ItemContent {
		item := observation.Item{Type: "mcpToolCall", Server: "docs", Tool: "search", Status: status}
		return presentation.ItemContent{Seq: seq, Kind: "tool", Text: presentation.MCPText(item),
			Command: presentation.Command{Source: seq, Running: running}, Details: &presentation.ItemDetails{Status: status}}
	}
	var calls []Block
	for i, call := range []presentation.ItemContent{mcp(1, "completed", false), mcp(2, "failed", false), mcp(3, "inProgress", true)} {
		blocks := values(call)
		if len(blocks) != 1 || blocks[0].Verb != "MCP" {
			t.Fatalf("call %d = %+v", i, blocks)
		}
		calls = append(calls, blocks...)
	}
	if grouped := GroupOperations(calls); grouped[1].GroupHeader != "" || grouped[2].GroupHeader != "" {
		t.Fatalf("failed or running call joined the completed group: %+v", grouped)
	}
	if !calls[1].FailedOutcome() || strings.Contains(calls[1].Label, "failed") {
		t.Fatalf("failed call outcome = %+v", calls[1].Value)
	}
	paint(calls...)

	// Failed siblings: every kind turns its verb red and names `· exit N`.
	for _, source := range []string{"cat missing.go", "rg needle missing", "skills-mgr get missing", "go vet ./..."} {
		blocks := values(presentation.ItemContent{Seq: 4, Kind: "tool", Text: presentation.ShellSource(source, ""),
			Command: presentation.Command{Source: 4, Script: source, Display: source, Exited: true, ExitCode: 2}, Details: &presentation.ItemDetails{CommandText: source}})
		paint(blocks...)
		if row := rows[len(rows)-1]; !strings.HasPrefix(row, theme.Danger()+"\x1b[1m") || !strings.Contains(ansi.Strip(row), "· exit 2") || strings.Contains(row, "(exit") {
			t.Fatalf("failed %q row = %q", source, row)
		}
	}
	paint(Block{Value: presentation.Value{Content: presentationContent("op", "Edit", "`a.go`"), Exited: true, ExitCode: 1}})

	// A search that found nothing exits 1 without failing.
	noMatch := values(presentation.ItemContent{Seq: 5, Kind: "tool", Text: presentation.ShellSource("rg absent src", ""),
		Command: presentation.Command{Source: 5, Script: "rg absent src", Display: "rg absent src", Exited: true, ExitCode: 1},
		Details: &presentation.ItemDetails{CommandText: "rg absent src", SearchResults: new(0)}})
	if len(noMatch) != 1 || noMatch[0].FailedOutcome() || noMatch[0].ExitFailed() {
		t.Fatalf("no-match search = %+v", noMatch)
	}
	paint(noMatch...)
	read := values(presentation.ItemContent{Seq: 6, Kind: "tool", Text: presentation.ShellSource("cat a.go", ""),
		Command: presentation.Command{Source: 6, Script: "cat a.go", Display: "cat a.go", Exited: true}, Details: &presentation.ItemDetails{CommandText: "cat a.go"}})
	batch, ok := CollapseBatch(append(append([]Block{}, noMatch...), read...))
	if !ok {
		t.Fatal("settled operations did not collapse")
	}
	rows = append(rows, p.Block(batch, 72)...)
	if strings.Contains(ansi.Strip(rows[len(rows)-1]), "failed") {
		t.Fatalf("no-match search counted as a failure: %q", rows[len(rows)-1])
	}

	// A settled command without an exit is not a success and keeps its output.
	unknown := values(presentation.ItemContent{Seq: 7, Kind: "tool", Text: presentation.ShellSource("make build", ""),
		Command: presentation.Command{Source: 7, Script: "make build", Display: "make build", Duration: 2 * time.Second, Tail: []string{"compiling", "linking"}},
		Details: &presentation.ItemDetails{CommandText: "make build"}})
	if len(unknown) != 1 || !unknown[0].ExitUnknown() || unknown[0].Collapsible() {
		t.Fatalf("unknown exit = %+v", unknown)
	}
	paint(unknown...)

	// Running and settled commands keep their command in one column.
	running := Block{Value: presentation.Value{Content: presentationContent("op", "Run", ""), Running: true, Started: time.Unix(1000, 0)}}
	running.Code, running.Lang, running.Fenced = "go test ./...", "bash", true
	ran := running
	ran.Running, ran.Exited, ran.Duration = false, true, 5*time.Second
	before, after := ansi.Strip(p.Block(AlignVerbs([]Block{running})[0], 72)[0]), ansi.Strip(p.Block(AlignVerbs([]Block{ran})[0], 72)[0])
	if strings.Index(before, "go test") != strings.Index(after, "go test") {
		t.Fatalf("command moved on completion: %q -> %q", before, after)
	}
	paint(running)
	paint(ran)

	// Amber marks live and waiting work only.
	sleep := Block{Value: presentation.Value{Content: presentationContent("op", "Sleep", "`5s`"), Running: true}}
	paint(sleep)
	sleep.Running = false
	paint(sleep)
	for _, status := range []string{"blocked", "stopped"} {
		hook := Block{Value: presentation.Value{Content: presentationContent("op", "Run", "PreToolUse · `hooks.toml`"), Hook: &HookDetails{HandlerType: "command", Status: status}}}
		paint(hook)
		if row := rows[len(rows)-1]; !strings.HasPrefix(row, theme.Danger()) {
			t.Fatalf("%s hook tone = %q", status, row)
		}
	}
	rows = append(rows, p.Block(Block{Value: presentation.Value{Content: presentationContent("compaction", "", "Context compacted at 120000 tokens (60%)")}}, 72)...)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/operation_outcomes.txt", append(rows, "plain after outcomes"), 72)
}

// Wait outcomes that did not complete stand out from the muted progress row.
func TestUISnapshotWaitOutcomeTones(t *testing.T) {
	p := Painter{Theme: diffrender.DarkTheme}
	var rows []string
	for _, block := range []Block{
		{Value: presentation.Value{Content: feed.Content{Kind: "progress", Body: "Finished waiting for"}, WaitTargets: []WaitTarget{{Name: "/root/worker", Status: "completed"}}, WaitReason: "completed"}},
		{Value: presentation.Value{Content: feed.Content{Kind: "progress", Body: "Finished waiting for"}, WaitTargets: []WaitTarget{{Name: "/root/worker", Status: "errored"}}}},
		{Value: presentation.Value{Content: feed.Content{Kind: "progress", Body: "Finished waiting for"}, WaitTargets: []WaitTarget{{Name: "/root/worker"}}, WaitReason: "timeout"}},
		{Value: presentation.Value{Content: feed.Content{Kind: "progress", Body: "Finished waiting for"}, WaitTargets: []WaitTarget{{Name: "/root/worker"}}, WaitReason: "delivery failed"}},
		{Value: presentation.Value{Content: feed.Content{Kind: "progress", Body: "Finished waiting for"}, WaitTargets: []WaitTarget{{Name: "/root/worker", Status: "working"}}, WaitReason: "message received"}},
	} {
		rows = append(rows, p.Block(block, 72)...)
	}
	if got := strings.ReplaceAll(ansi.Strip(rows[len(rows)-1]), "\u00a0", " "); got != "↔ Finished waiting for worker: working · message received" {
		t.Fatalf("aggregate reason = %q", got)
	}
	// Narrow rows hang under the text rather than the marker.
	narrow := p.Block(Block{Value: presentation.Value{Content: feed.Content{Kind: "progress", Body: "Finished waiting for"}, WaitTargets: []WaitTarget{{Name: "/root/worker", Status: "failed"}}}}, 24)
	for _, row := range narrow[1:] {
		if !strings.HasPrefix(ansi.Strip(row), "  ") {
			t.Fatalf("wrapped wait row is not hung: %q", narrow)
		}
	}
	rows = append(rows, narrow...)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/wait_outcome_tones.txt", append(rows, "plain after waits"), 72)
}

func presentationContent(kind, verb, label string) feed.Content {
	return feed.Content{Kind: kind, Verb: verb, Label: label}
}

// A for loop's semicolons are not top-level statements, so a narrow preview
// breaks only between the loop and the statement after it.
func TestRanRowKeepsCompoundStatementsTogether(t *testing.T) {
	p := Painter{}
	block := Block{Value: presentation.Value{Content: feed.Content{Kind: "op", Verb: "Run", Code: "for f in a b c; do echo $f; done; echo end", Lang: "bash", Fenced: true}}}
	rows := p.Block(block, 48)
	var plain []string
	for _, row := range rows {
		plain = append(plain, ansi.Strip(row))
	}
	want := []string{"Ran     for f in a b c; do echo $f; done;", "        echo end"}
	if strings.Join(plain, "\n") != strings.Join(want, "\n") {
		t.Fatalf("loop rows = %q, want %q", plain, want)
	}
}

// Hidden source and hidden output share one elision row.
func TestRanRowJoinsSourceAndOutputElisions(t *testing.T) {
	p := Painter{}
	block := Block{Value: presentation.Value{Content: feed.Content{Kind: "op", Verb: "Run", Code: "go test ./internal/router ./internal/ui ./internal/session -run Native -count=1", Lang: "bash", Fenced: true},
		Exited: true, Tail: []string{"ok a", "ok b", "ok c", "ok d"}}}
	block.SourceRows, block.Collapsed = 2, true
	rows := p.Block(block, 30)
	var plain []string
	for _, row := range rows {
		plain = append(plain, ansi.Strip(row))
	}
	last := plain[len(plain)-1]
	if len(plain) != 2 || !strings.HasPrefix(strings.TrimSpace(last), "… +") || !strings.HasSuffix(last, "lines ┆ +4 lines") {
		t.Fatalf("elision rows = %q", plain)
	}
}

// A clipped answer keeps its frame; the elision counts hidden content only.
func TestUISnapshotClippedAnswerCard(t *testing.T) {
	v := NewView()
	v.ChildrenOnly = true
	v.Painter.Theme = diffrender.DarkTheme
	item := presentation.ItemContent{Seq: 1, Agent: "/root/worker", Kind: "final", Text: "one\n\ntwo\n\nthree\n\nfour\n\nfive", Observed: time.Unix(100, 0)}
	v.Update(&presentation.Publication{Entries: []presentation.Entry{{Facts: item.Facts, Values: item.Values()}}}, Display{Caller: "/root"})
	feed := v.LayoutFeed(40)
	plain := ansi.Strip(strings.Join(feed.Lines, "\n"))
	if !strings.Contains(plain, "╰") || !strings.Contains(plain, "… +7 lines") {
		t.Fatalf("clipped card lost its frame or miscounted:\n%s", plain)
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/clipped_answer_card.txt", append(feed.Lines, "plain after card"), 40)
}
