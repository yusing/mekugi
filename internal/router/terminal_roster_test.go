package router

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestTerminalGeometrySeparatesRosterFromCodexAndFeed(t *testing.T) {
	const width, height, split, feedSplit, rosterHeight = 160, 44, 68, 26, 10
	l := terminalGeometry(width, height, split, feedSplit, rosterHeight, 0, true, true)
	if l.codex.x != 0 || l.codex.y != 0 || l.codex.w != split || l.codex.h <= 0 {
		t.Fatalf("Codex rect = %+v", l.codex)
	}
	if l.roster.x != 0 || l.roster.w != split || l.roster.h != rosterHeight || l.roster.y != l.rosterHorizontal+1 {
		t.Fatalf("roster rect = %+v, divider row = %d", l.roster, l.rosterHorizontal)
	}
	if l.codex.y+l.codex.h != l.rosterHorizontal || l.roster.y+l.roster.h != height-1 {
		t.Fatalf("Codex and roster do not fill the left column: Codex=%+v roster=%+v divider=%d", l.codex, l.roster, l.rosterHorizontal)
	}
	if l.diff.x != split+1 || l.diff.y != 0 || l.diff.h != feedSplit ||
		l.agents.x != split+1 || l.agents.y != feedSplit+1 || l.agents.h != height-feedSplit-2 {
		t.Fatalf("right feed geometry changed: diff=%+v agents=%+v", l.diff, l.agents)
	}
	if l.vertical != split || l.horizontal != feedSplit {
		t.Fatalf("right-side split handles = (%d,%d), want (%d,%d)", l.vertical, l.horizontal, split, feedSplit)
	}
	if rectsOverlap(l.codex, l.roster) || rectsOverlap(l.roster, l.diff) || rectsOverlap(l.roster, l.agents) {
		t.Fatalf("terminal regions overlap: %+v", l)
	}

	shorter := terminalGeometry(width, height, split, feedSplit, rosterHeight-3, 0, true, true)
	if shorter.diff != l.diff || shorter.agents != l.agents {
		t.Fatalf("resizing the left roster changed the right feed: before=%+v after=%+v", l, shorter)
	}

	narrow := terminalGeometry(80, height, split, feedSplit, rosterHeight, 3, true, true)
	if narrow.roster != (terminalRect{w: 80, h: height - 1}) || narrow.codex.w != 0 || narrow.diff.w != 0 || narrow.agents.w != 0 {
		t.Fatalf("narrow roster focus did not occupy the terminal: %+v", narrow)
	}
}

func rectsOverlap(a, b terminalRect) bool {
	return a.w > 0 && a.h > 0 && b.w > 0 && b.h > 0 &&
		a.x < b.x+b.w && b.x < a.x+a.w && a.y < b.y+b.h && b.y < a.y+a.h
}

func TestTerminalRosterFocusAndDragResize(t *testing.T) {
	ui := &terminalUI{
		agents:       liveActivityTestView("/root/alpha", "/root/beta"),
		width:        160,
		height:       44,
		split:        68,
		horizontal:   26,
		rosterHeight: 10,
		side:         true,
		activityOpen: true,
	}
	if err := ui.key(2); err != nil {
		t.Fatal(err)
	}
	if err := ui.key('4'); err != nil {
		t.Fatal(err)
	}
	if ui.focus != 3 || !ui.side || !ui.activityOpen {
		t.Fatalf("Ctrl-B 4 did not focus/open the roster: focus=%d side=%v activity=%v", ui.focus, ui.side, ui.activityOpen)
	}
	ui.layout = terminalGeometry(ui.width, ui.height, ui.split, ui.horizontal, ui.rosterHeight, 3, ui.side, ui.activityOpen)
	startHeight := ui.rosterHeight
	x, y := 10, ui.layout.rosterHorizontal
	if err := ui.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1)); err != nil {
		t.Fatal(err)
	}
	if ui.drag != 4 {
		t.Fatalf("roster divider drag mode = %d, want 4", ui.drag)
	}
	if err := ui.mouse(fmt.Sprintf("\x1b[<32;%d;%dM", x+1, y+4)); err != nil {
		t.Fatal(err)
	}
	if ui.rosterHeight == startHeight {
		t.Fatalf("dragging the roster divider did not resize it: %d", ui.rosterHeight)
	}
	if err := ui.mouse(fmt.Sprintf("\x1b[<0;%d;%dm", x+1, y+4)); err != nil {
		t.Fatal(err)
	}
	if ui.drag != 0 {
		t.Fatalf("roster divider drag did not end: mode=%d", ui.drag)
	}
}

func TestTerminalRosterKeyboardNavigationAndPrefixResize(t *testing.T) {
	view := liveActivityTestView("/root/alpha", "/root/beta")
	ui := &terminalUI{
		agents:       view,
		width:        160,
		height:       44,
		split:        68,
		horizontal:   26,
		rosterHeight: 10,
		focus:        3,
		side:         true,
		activityOpen: true,
	}
	for _, key := range []string{"j", "j", "k"} {
		if err := ui.send(key); err != nil {
			t.Fatal(err)
		}
	}
	if !view.only || view.selected != "/root/alpha" {
		t.Fatalf("roster j/k navigation = only:%v selected:%q", view.only, view.selected)
	}
	if err := ui.send("k"); err != nil {
		t.Fatal(err)
	}
	if view.only || !view.visible(activityPaneEntry{Agent: "/root/beta"}) {
		t.Fatalf("k from first agent did not restore all-agents endpoint: only=%v selected=%q", view.only, view.selected)
	}

	startHeight, feedSplit, mainSplit := ui.rosterHeight, ui.horizontal, ui.split
	if err := ui.key(2); err != nil {
		t.Fatal(err)
	}
	for _, key := range []byte("\x1b[A") {
		if err := ui.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if ui.rosterHeight != startHeight+1 || ui.horizontal != feedSplit || ui.split != mainSplit {
		t.Fatalf("Ctrl-B ↑ resized the wrong region: roster=%d feed=%d main=%d", ui.rosterHeight, ui.horizontal, ui.split)
	}
}

func TestLiveActivitySeparateRosterSharesSelectionAndFeedState(t *testing.T) {
	view := liveActivityTestView("/root/alpha", "/root/beta")
	view.feedOnly = true
	now := time.Now()
	feed := view.render(90, 14, now)
	if !strings.Contains(strings.Join(plainLines(feed), "\n"), "Read") {
		t.Fatalf("feed-only view lost its activity feed: %q", plainLines(feed))
	}
	if len(view.hits) != 0 {
		t.Fatalf("feed view retained roster pointer hits: %+v", view.hits)
	}
	feedTop, feedLeft, feedRight := view.feedTop, view.feedLeft, view.feedRight
	feedLines, feedRows := view.feedLines, view.feedRows
	feedSnippets := slices.Clone(view.feedSnippets)

	roster := view.renderRosterPane(55, 10, now)
	if len(roster) != 10 {
		t.Fatalf("roster pane rows = %d, want 10", len(roster))
	}
	if plain := strings.Join(plainLines(roster), "\n"); !strings.Contains(plain, "alpha") || !strings.Contains(plain, "beta") {
		t.Fatalf("separate roster omitted agents: %s", plain)
	}
	if view.feedTop != feedTop || view.feedLeft != feedLeft || view.feedRight != feedRight ||
		view.feedLines != feedLines || view.feedRows != feedRows || !slices.Equal(view.feedSnippets, feedSnippets) {
		t.Fatalf("roster rendering overwrote feed hit-test/scroll metadata")
	}
	var betaHit liveActivityHit
	for _, hit := range view.hits {
		if hit.agent == "/root/beta" {
			betaHit = hit
			break
		}
	}
	if betaHit.agent == "" {
		t.Fatalf("separate roster did not publish its own agent hit regions: %+v", view.hits)
	}
	ui := &terminalUI{
		agents:       view,
		width:        160,
		height:       44,
		split:        68,
		horizontal:   26,
		rosterHeight: 10,
		side:         true,
		activityOpen: true,
	}
	ui.layout = terminalGeometry(ui.width, ui.height, ui.split, ui.horizontal, ui.rosterHeight, 0, ui.side, ui.activityOpen)
	// The UI routes pane-3 clicks straight to the separate roster's hit regions;
	// feedOnly deliberately disables roster hit testing in the right-side feed.
	x := ui.layout.roster.x + betaHit.first
	y := ui.layout.roster.y + betaHit.row
	if err := ui.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x, y)); err != nil {
		t.Fatal(err)
	}
	if ui.focus != 3 || !view.only || view.selected != "/root/beta" || view.visible(activityPaneEntry{Agent: "/root/alpha"}) || !view.visible(activityPaneEntry{Agent: "/root/beta"}) {
		t.Fatalf("roster click did not filter its shared feed state: selected=%q only=%v", view.selected, view.only)
	}
}

func TestTerminalPaintPlacesRosterLeftAndAgentsFeedRight(t *testing.T) {
	const width, height, split, feedSplit, rosterHeight = 160, 44, 68, 26, 10
	l := terminalGeometry(width, height, split, feedSplit, rosterHeight, 0, true, true)
	ui := &terminalUI{
		codex:        vt.NewEmulator(l.codex.w, l.codex.h),
		diffScreen:   vt.NewEmulator(l.diff.w, l.diff.h),
		diffFailure:  "fixture skips live diff rendering",
		agents:       liveActivityTestView("/root/alpha", "/root/beta"),
		width:        width,
		height:       height,
		split:        split,
		horizontal:   feedSplit,
		rosterHeight: rosterHeight,
		side:         true,
		activityOpen: true,
	}
	defer ui.codex.Close()
	defer ui.diffScreen.Close()
	var frame bytes.Buffer
	if err := ui.paint(t.Context(), &frame); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(frame.Bytes(), []byte("\x1b[7m")) || bytes.Contains(bytes.ToLower(frame.Bytes()), []byte("drag borders")) {
		t.Fatalf("terminal status used inverse styling or exposed a drag-borders hint: %q", frame.Bytes())
	}
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	if _, err := screen.Write(frame.Bytes()); err != nil {
		t.Fatal(err)
	}
	lines := plainLines(strings.Split(screen.Render(), "\n"))
	regionText := func(r terminalRect) string {
		var rows []string
		for y := r.y; y < r.y+r.h && y < len(lines); y++ {
			rows = append(rows, ansi.Cut(lines[y], r.x, r.x+r.w))
		}
		return strings.Join(rows, "\n")
	}
	left, right := regionText(ui.layout.roster), regionText(ui.layout.agents)
	if !strings.Contains(left, "alpha") || !strings.Contains(left, "beta") {
		t.Fatalf("left roster region does not contain both agents: %q", left)
	}
	if !strings.Contains(right, "Read") {
		t.Fatalf("right activity region does not contain feed entries: %q", right)
	}
	if ansi.StringWidth(lines[ui.layout.roster.y]) > width {
		t.Fatalf("roster paint exceeded terminal width: %q", lines[ui.layout.roster.y])
	}
}
