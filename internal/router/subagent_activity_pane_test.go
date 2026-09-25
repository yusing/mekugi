package router

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

type activityPaneFixture struct {
	activity *subagentActivity
	pane     *activityPane
	server   *httptest.Server
	launches int
}

func newActivityPaneFixture(t *testing.T, available bool) *activityPaneFixture {
	t.Helper()
	f := &activityPaneFixture{activity: newSubagentActivity()}
	f.activity.usage = newThreadUsage()
	f.pane = newActivityPane(t.Context(), func() bool { f.launches++; return available })
	f.activity.attachPane(f.pane)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+liveActivityEventsPath, f.activity.serveActivityPane)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	f.activity.setPaneEndpoint(f.server.URL + liveActivityEventsPath)
	f.activity.observe("root", "", "/root", false)
	f.activity.observe("explorer", "root", "/root/explorer", true)
	f.activity.observe("probe", "explorer", "/root/explorer/probe", true)
	return f
}

type activityPaneClient struct {
	cancel  context.CancelFunc
	scanner *bufio.Scanner
}

func (f *activityPaneFixture) connect(t *testing.T) *activityPaneClient {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	connection := f.activity.paneDescriptor()
	req, err := liveDiffRequest(ctx, connection, http.MethodGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	client := &activityPaneClient{cancel: cancel, scanner: bufio.NewScanner(response.Body)}
	t.Cleanup(func() { cancel(); response.Body.Close() })
	return client
}

func (c *activityPaneClient) next(t *testing.T, kinds ...string) activityPaneEvent {
	t.Helper()
	result := make(chan activityPaneEvent, 1)
	go func() {
		for c.scanner.Scan() {
			var event activityPaneEvent
			if json.Unmarshal(c.scanner.Bytes(), &event) == nil && strings.Contains(strings.Join(kinds, ","), event.Kind) {
				result <- event
				return
			}
		}
		close(result)
	}()
	select {
	case event, ok := <-result:
		if !ok {
			t.Fatal("activity stream closed")
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for activity event")
	}
	return activityPaneEvent{}
}

func drainText(messages []map[string]json.RawMessage) string {
	var parts []string
	for _, message := range messages {
		var content []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(message["content"], &content)
		for _, part := range content {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n---\n")
}

func TestActivityPaneOwnsChildActivityAndDeliversWithoutRootBoundary(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-effective", "input": []any{journalTestAssignment("/root/explorer", "NEW_TASK", "Inspect parser.\n\n- Preserve behavior.")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	start := subagentStartCommentary(&request, "/root/explorer")
	f.activity.collect("explorer", "start", "start", start)
	if f.launches != 1 {
		t.Fatalf("launches = %d, want 1", f.launches)
	}
	// Claimed but not yet attached: the root receives neither copies nor notices.
	if got := f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes); len(got) != 0 {
		t.Fatalf("claimed pane leaked root copies: %s", drainText(got))
	}

	client := f.connect(t)
	snapshot := client.next(t, "snapshot")
	if len(snapshot.Agents) != 3 || snapshot.Agents[0].Name != "/root" || snapshot.Agents[1].Name != "/root/explorer" || snapshot.Agents[2].Name != "/root/explorer/probe" {
		t.Fatalf("snapshot roster = %+v", snapshot.Agents)
	}
	first := client.next(t, "entries")
	if len(first.Entries) != 1 || first.Entries[0].Agent != "/root/explorer" || first.Entries[0].Text != start {
		t.Fatalf("first entries = %+v", first.Entries)
	}

	// No root response is open (a native wait): the viewer still gets updates.
	f.activity.collect("probe", "tool-1", "tool", "Read `a.go`")
	live := client.next(t, "entries")
	if len(live.Entries) != 1 || live.Entries[0].Agent != "/root/explorer/probe" || live.Entries[0].Text != "Read `a.go`" {
		t.Fatalf("live entries = %+v", live.Entries)
	}

	announce := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes))
	if !strings.Contains(announce, "Mekugi agents pane") || strings.Contains(announce, "a.go") {
		t.Fatalf("root drain while attached = %q", announce)
	}
	if again := f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes); len(again) != 0 {
		t.Fatalf("announcement repeated: %s", drainText(again))
	}

	// Roster state is observation only.
	f.activity.beginResponse("probe")
	state := client.next(t, "agents", "entries")
	if !state.Agents[2].Responding {
		t.Fatalf("responding not reported: %+v", state.Agents)
	}
	if state.Agents[2].Turns != 1 || state.Agents[2].Started.IsZero() {
		t.Fatalf("turn timer not reported: %+v", state.Agents[2])
	}
	// Streamed deltas show an estimate on the next tick, without a wake.
	f.activity.streamOutput("probe", 400)
	estimate := client.next(t, "agents")
	for estimate.Agents[2].OutputTokens != 400/activityBytesPerToken {
		estimate = client.next(t, "agents")
	}
	// Usage replaces the estimate, accumulates per child, and reaches the
	// roster without new entries.
	for _, counts := range []tokenCounts{{InputTokens: 1200, UncachedInputTokens: 1200, OutputTokens: 30}, {InputTokens: 800, UncachedInputTokens: 800, OutputTokens: 20}} {
		f.activity.usage.observation("probe", "", "gpt-6-sol", "").observe(counts)
		f.activity.syncUsage("probe")
	}
	usage := client.next(t, "agents")
	for usage.Agents[2].InputTokens != 2000 {
		usage = client.next(t, "agents")
	}
	if usage.Agents[2].OutputTokens != 50 || usage.Agents[0].InputTokens != 0 {
		t.Fatalf("usage roster = %+v", usage.Agents)
	}
	if usage.Agents[2].Cost <= 0 || !usage.Agents[2].CostKnown {
		t.Fatalf("cost roster = %+v", usage.Agents[2])
	}
	f.activity.endResponse("probe")
	f.activity.markFinal("explorer", subagentFinal{sender: "/root/explorer/probe"})
	final := client.next(t, "agents")
	for final.Agents[2].Responding || !final.Agents[2].Final {
		final = client.next(t, "agents")
	}
	if final.Agents[2].LastResponse.IsZero() {
		t.Fatalf("last response not reported: %+v", final.Agents[2])
	}

	// Closing the sole view releases ownership; subsequent activity returns inline.
	client.cancel()
	waitActivityPaneState(t, f, activityPaneReleased)
	f.activity.collect("explorer", "tool-2", "tool", "Search `needle`")
	f.activity.collect("probe", "tool-3", "tool", "Read `b.go`")
	inline := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes))
	if !strings.Contains(inline, "pane closed") || !strings.Contains(inline, "Search `needle`") ||
		!strings.Contains(inline, "/root/explorer/probe") || !strings.Contains(inline, "b.go") {
		t.Fatalf("fallback drain = %q", inline)
	}
	f.activity.collect("explorer", "tool-4", "tool", "Read `c.go`")
	if f.launches != 1 {
		t.Fatalf("released pane relaunched: %d", f.launches)
	}
	req, _ := liveDiffRequest(t.Context(), f.activity.paneDescriptor(), http.MethodGet, nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusGone {
		t.Fatalf("released pane accepted a viewer: %d", response.StatusCode)
	}
}

func TestActivityPaneShowsFinalAnswerWithoutRootCopy(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	final := subagentFinal{sender: "/root/explorer/probe", source: "answer-1", text: "The final result is ready.\n\n- verified"}
	f.activity.markFinal("explorer", final)
	f.activity.markFinal("explorer", final) // Replayed input must not duplicate the pane event.
	client := f.connect(t)
	snapshot := client.next(t, "snapshot")
	if !snapshot.Agents[2].Final {
		t.Fatalf("final marker missing: %+v", snapshot.Agents)
	}
	entries := client.next(t, "entries")
	if len(entries.Entries) != 1 || entries.Entries[0].Kind != "final" || entries.Entries[0].Text != final.text {
		t.Fatalf("final pane entry = %+v", entries.Entries)
	}
	view := newLiveActivityView()
	view.apply(snapshot)
	view.apply(entries)
	frame := strings.Join(plainLines(view.render(80, 20, time.Now())), "\n")
	if !strings.Contains(frame, "The final result is ready") || !strings.Contains(frame, "verified") {
		t.Fatalf("final content absent from pane: %s", frame)
	}
	// The status glyph marks the final answer; the summary does not repeat it.
	for line := range strings.SplitSeq(frame, "\n") {
		if strings.Contains(line, "The final result is ready") && strings.Count(line, "✓") > 1 {
			t.Fatalf("roster repeats the final marker: %q", line)
		}
	}
	if got := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes)); strings.Contains(got, final.text) {
		t.Fatalf("native completion copied into commentary: %q", got)
	}

	withoutPane := newActivityPaneFixture(t, false)
	withoutPane.activity.markFinal("explorer", final)
	if got := drainText(withoutPane.activity.drain("root", time.Now(), maxCommentaryPublicationBytes)); strings.Contains(got, final.text) {
		t.Fatalf("native completion copied without pane: %q", got)
	}
	large := subagentFinal{sender: final.sender, source: "answer-large", text: strings.Repeat("界", maxCommentaryPublicationBytes/3)}
	withoutPane.activity.markFinal("explorer", large)
	withoutPane.activity.mu.Lock()
	defer withoutPane.activity.mu.Unlock()
	if len(withoutPane.activity.events) != 1 || !strings.Contains(withoutPane.activity.events[0].raw, "full answer in Codex completion") {
		t.Fatalf("large final answer lost from bounded pane preview: %+v", withoutPane.activity.events)
	}
}

func waitActivityPaneState(t *testing.T, f *activityPaneFixture, want activityPaneState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.activity.mu.Lock()
		state := f.pane.state
		f.activity.mu.Unlock()
		if state == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pane state did not become %d", want)
}

func TestActivityPaneUnavailableKeepsInlineDelivery(t *testing.T) {
	f := newActivityPaneFixture(t, false)
	f.activity.collect("explorer", "tool-1", "tool", "Read `a.go`")
	got := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes))
	if !strings.Contains(got, "/root/explorer") || !strings.Contains(got, "a.go") || strings.Contains(got, "pane") {
		t.Fatalf("inline drain = %q", got)
	}
}

func TestActivityPaneLaunchTimeoutReturnsInline(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	f.activity.collect("explorer", "tool-1", "tool", "Read `a.go`")
	f.activity.releasePane()
	got := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes))
	// The viewer never attached, so there is no closing notice to explain.
	if !strings.Contains(got, "a.go") || strings.Contains(got, "pane") {
		t.Fatalf("drain after failed launch = %q", got)
	}
}

func TestActivityPaneDivertsRootReplies(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	f.activity.collect("explorer", "start", "start", "Started")
	message := assistantCommentaryMessage("reply-1", "[`/root/explorer` -> `/root`] Message received:\nhello")
	kept := f.activity.divertRootReplies("root", []map[string]json.RawMessage{message}, []string{"/root/explorer"})
	if len(kept) != 0 {
		t.Fatalf("reply stayed inline: %v", kept)
	}
	client := f.connect(t)
	client.next(t, "snapshot")
	client.next(t, "entries") // The start event is delivered separately.
	entries := client.next(t, "entries").Entries
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Text, "[`/root/explorer` -> `/root`]") {
		t.Fatalf("diverted reply = %+v", entries)
	}

	// Unowned roots keep the reply in the root response.
	other := newActivityPaneFixture(t, false)
	if kept := other.activity.divertRootReplies("root", []map[string]json.RawMessage{message}, []string{"/root/explorer"}); len(kept) != 1 {
		t.Fatal("unowned root reply was diverted")
	}
}

func liveActivityTestView(names ...string) *liveActivityView {
	view := newLiveActivityView()
	var agents []activityPaneAgent
	var entries []activityPaneEntry
	now := time.Now()
	for i, name := range names {
		agents = append(agents, activityPaneAgent{Name: name})
		entries = append(entries, activityPaneEntry{Seq: uint64(i + 1), Agent: name, Kind: "tool", Text: "Read `" + name + ".go`", Observed: now})
	}
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: agents, Entries: entries})
	return view
}

func plainLines(lines []string) []string {
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = ansi.Strip(line)
	}
	return plain
}

func TestLiveActivityViewRosterTreeOverflowAndSelection(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b", "/root/a/x", "/root/c", "/root/a/y", "/root/d", "/root/e")
	view.agents[0].Responding = true
	view.agents[1].Final = true
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 8, Agent: "/root/c", Kind: "error", Text: "failed", Observed: time.Now()}}})
	var order []string
	for _, row := range view.roster() {
		order = append(order, strings.Repeat(">", row.depth)+row.agent.Name)
	}
	if got := strings.Join(order, " "); got != "/root/a >/root/a/x >/root/a/y /root/b /root/c /root/d /root/e" {
		t.Fatalf("roster order = %s", got)
	}
	lines := plainLines(view.render(60, 20, time.Now()))
	if !strings.HasPrefix(lines[0], "AGENTS  7 agents · 1 responding · 1 error") || !strings.HasSuffix(lines[0], "FOLLOW") {
		t.Fatalf("header = %q", lines[0])
	}
	// Six rows compact to five agents and directional overflow.
	if !strings.HasPrefix(lines[1], "◐  a") || !strings.Contains(lines[2], "├ x") ||
		lines[6] != "↓ 2 more" {
		t.Fatalf("roster = %q", lines[1:7])
	}
	for range 6 {
		view.selectAgent(1)
	}
	styled := view.render(60, 20, time.Now())
	lines = plainLines(styled)
	// Selection fills its row in place, without a marker column.
	fill := view.painter.theme.SelectionBackground()
	if view.selected != "/root/e" || !slices.ContainsFunc(styled[1:7], func(line string) bool {
		return strings.HasPrefix(line, fill) && strings.Contains(ansi.Strip(line), "·  e")
	}) ||
		!slices.ContainsFunc(lines[1:7], func(line string) bool { return strings.HasPrefix(line, "·  e") }) {
		t.Fatalf("selection scroll: selected=%s roster=%q", view.selected, lines[1:7])
	}
	view.selectAgent(1)
	if view.selected != "/root/a" {
		t.Fatalf("selection did not wrap: %s", view.selected)
	}
	for _, line := range view.render(60, 20, time.Now()) {
		if ansi.StringWidth(line) > 59 {
			t.Fatalf("line exceeds pane: %q", ansi.Strip(line))
		}
	}
}

func TestLiveActivityViewClampOnlyModeAndPausedCount(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	long := "Plan\n```go\n" + strings.Repeat("line\n", 20) + "```"
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 3, Agent: "/root/a", Text: long, Observed: time.Now()}}})
	all := strings.Join(plainLines(view.render(80, 40, time.Now())), "\n")
	if !strings.Contains(all, "… +11 lines") || !strings.Contains(all, "│ line") {
		t.Fatalf("clamped feed = %s", all)
	}
	view.handleKey("", 'o')
	only := plainLines(view.render(80, 40, time.Now()))
	joined := strings.Join(only, "\n")
	if strings.Contains(joined, "+11 lines") || strings.Count(joined, "│ line") != 20 || strings.Contains(joined, "● b") {
		t.Fatalf("only feed = %s", joined)
	}
	if !strings.HasPrefix(only[len(only)-1], "ONLY ·") || !strings.Contains(only[0], "only a (1/2)") {
		t.Fatalf("only chrome = %q / %q", only[0], only[len(only)-1])
	}
	view.handleKey("", 'k')
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 4, Agent: "/root/a", Text: "more", Observed: time.Now()},
		{Seq: 5, Agent: "/root/b", Text: "hidden", Observed: time.Now()},
		{Seq: 4, Agent: "/root/a", Text: "duplicate", Observed: time.Now()},
	}})
	if header := plainLines(view.render(80, 40, time.Now()))[0]; !strings.HasSuffix(header, "PAUSED · 1 new") {
		t.Fatalf("paused header = %q", header)
	}
	view.handleKey("", 'r')
	if header := plainLines(view.render(80, 40, time.Now()))[0]; !strings.HasSuffix(header, "FOLLOW") {
		t.Fatalf("follow header = %q", header)
	}
}

func TestLiveActivitySnippetClick(t *testing.T) {
	long := "Plan\n```go\n" + strings.Repeat("line\n", 20) + "```"
	hint := regexp.MustCompile(`… \+\d+ lines$`)
	for _, size := range [][2]int{{80, 40}, {140, 40}} {
		view := liveActivityTestView("/root/a", "/root/b")
		now := time.Now()
		view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 3, Agent: "/root/a", Text: long, Observed: now}}})
		lines := view.render(size[0], size[1], now)
		clipped := func(lines []string) bool {
			return slices.ContainsFunc(plainLines(lines), func(line string) bool { return hint.MatchString(strings.TrimRight(line, " ")) })
		}
		row := slices.IndexFunc(plainLines(lines), func(line string) bool { return hint.MatchString(strings.TrimRight(line, " ")) }) + 1
		if row == 0 {
			t.Fatalf("%v: no clipped snippet: %q", size, plainLines(lines))
		}
		column := view.feedLeft + 4
		if !view.handleMouse('h', row-2, column) || !strings.Contains(view.render(size[0], size[1], now)[row-1], "\x1b[4m… +") {
			t.Fatalf("%v: hovering a collapsed snippet did not underline its hint", size)
		}
		if !view.handleMouse('h', 1, column) || view.render(size[0], size[1], now)[row-1] != lines[row-1] {
			t.Fatalf("%v: leaving the snippet kept its underline", size)
		}
		// Clicks outside a snippet leave it clipped.
		view.handleMouse('\r', 1, column)
		if !clipped(view.render(size[0], size[1], now)) {
			t.Fatalf("%v: header click expanded the snippet", size)
		}
		if !view.handleMouse('\r', row, column) {
			t.Fatalf("%v: click did not redraw", size)
		}
		expanded := view.render(size[0], size[1], now)
		if clipped(expanded) || strings.Count(strings.Join(plainLines(expanded), "\n"), "│ line") != 20 {
			t.Fatalf("%v: click did not expand: %q", size, plainLines(expanded))
		}
		// Hovering an expanded snippet underlines nothing; another click collapses it.
		view.handleMouse('h', row, column)
		if strings.Contains(strings.Join(view.render(size[0], size[1], now), "\n"), "\x1b[4m") {
			t.Fatalf("%v: expanded snippet underlined", size)
		}
		view.handleMouse('\r', row, column)
		if collapsed := view.render(size[0], size[1], now); !clipped(collapsed) {
			t.Fatalf("%v: second click did not collapse: %q", size, plainLines(collapsed))
		}
	}
}

func TestLiveActivityViewTinyAndNarrowPanes(t *testing.T) {
	view := liveActivityTestView("/root/explorer/deeply/nested/worker", "/root/b", "/root/c")
	for _, size := range [][2]int{{10, 3}, {24, 6}, {30, 9}} {
		lines := view.render(size[0], size[1], time.Now())
		if len(lines) != size[1] {
			t.Fatalf("%v: %d lines", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0]-1 {
				t.Fatalf("%v: line exceeds pane: %q", size, ansi.Strip(line))
			}
		}
	}
	if lines := plainLines(view.render(40, 6, time.Now())); !strings.HasPrefix(lines[1], "· explorer/deeply/nested/worker  · b") {
		t.Fatalf("short pane roster = %q", lines)
	}
	if got := liveActivityMiddle("/root/explorer/deeply/nested/worker", 20); !strings.HasSuffix(got, "…worker") || ansi.StringWidth(got) != 20 {
		t.Fatalf("middle = %q", got)
	}
}

func TestLiveActivityRosterShowsUsageByLayout(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 146_800, 3_200
	for _, width := range []int{80, 140} {
		lines := plainLines(view.render(width, 20, time.Now()))
		row := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "↑") })
		// The roster status ends the row; cards end it at the column divider.
		status, _, _ := strings.Cut(lines[max(0, row)], " │ ")
		want := "↑ 146.8K ↓ 3.2K"
		if width >= 100 {
			want = "↑146.8K"
		}
		if row < 0 || !strings.HasSuffix(strings.TrimRight(status, " "), want) {
			t.Fatalf("width %d: usage missing: %q", width, lines)
		}
		if slices.ContainsFunc(lines, func(line string) bool { return strings.Count(line, "↑") > 1 }) {
			t.Fatalf("width %d: agent without usage shows tokens: %q", width, lines)
		}
	}
}

func TestLiveActivityCombinedRosterShowsTimerCostAndUsage(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	view := liveActivityTestView("/root/a", "/root/b")
	view.agents[0].Started = now.Add(-8 * time.Minute)
	view.agents[0].LastResponse = now.Add(-3 * time.Second)
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 1_000, 0
	view.agents[0].Turns, view.agents[0].Cost, view.agents[0].CostKnown = 2, 1.2, true
	view.agents[1].Started = now.Add(-7 * time.Minute)
	view.agents[1].LastResponse = now.Add(-2 * time.Minute)
	view.agents[1].InputTokens, view.agents[1].OutputTokens = 500, 0
	view.agents[1].Turns, view.agents[1].Cost, view.agents[1].CostKnown = 1, 0, false
	lines := plainLines(view.render(90, 20, now))
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	first := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "$1.2000") })
	second := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "7m · ") })
	// Unknown cost is omitted rather than shown as n/a or zero.
	if first < 0 || second < 0 || first != second-1 || !strings.Contains(lines[first], "8m · 3s ago ↑ 1K ↓ 0") ||
		!strings.Contains(lines[first], "$1.2000 T+2") || !strings.Contains(lines[second], "7m · 2m ago") ||
		!strings.Contains(lines[second], "↑ 500 ↓ 0 T+1") || strings.Contains(lines[second], "$") || strings.Contains(lines[second], "n/a") {
		t.Fatalf("combined roster metrics = %q", lines)
	}
}

func TestLiveActivityRosterUsesAvailableWidth(t *testing.T) {
	view := liveActivityTestView("/root/review_stock_preview")
	lines := plainLines(view.render(80, 20, time.Now()))
	if !strings.Contains(lines[1], "review_stock_preview  ") || strings.Contains(lines[1], "…") {
		t.Fatalf("roster name clipped or padded unexpectedly: %q", lines[1])
	}
	view = liveActivityTestView("/root/very/long/agent/name/that/could/eat/the/whole/roster", "/root/b")
	lines = plainLines(view.render(60, 20, time.Now()))
	if !strings.Contains(lines[2], "Read b.go") {
		t.Fatalf("long name obscured short agent's summary: %q", lines[2])
	}
}

func TestLiveActivityTerminalProcess(t *testing.T) {
	if os.Getenv("MEKUGI_LIVE_ACTIVITY_TEST_CHILD") == "1" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		os.Exit(RunLiveActivity(ctx, []string{"--session-file", os.Getenv("MEKUGI_LIVE_ACTIVITY_SESSION")}, os.Stdin, os.Stdout, os.Stderr))
	}
	f := newActivityPaneFixture(t, true)
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-effective", "input": []any{journalTestAssignment("/root/explorer", "NEW_TASK", "Inspect parser.\n\n- Preserve behavior.")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	f.activity.collect("explorer", "start", "start", subagentStartCommentary(&request, "/root/explorer"))
	session := filepath.Join(t.TempDir(), "activity.json")
	data, err := json.Marshal(f.activity.paneDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, data, 0600); err != nil {
		t.Fatal(err)
	}
	const height = 24
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveActivityTerminalProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_ACTIVITY_TEST_CHILD=1", "MEKUGI_LIVE_ACTIVITY_SESSION="+session)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: height, Cols: 100})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); terminal.Close(); <-done })
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		var buffer [8192]byte
		for {
			n, err := terminal.Read(buffer[:])
			if n > 0 {
				chunks <- string(buffer[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	h := &liveDiffTerminalHarness{ctx: ctx, pty: terminal, chunks: chunks, height: height, done: done}
	go func() { h.waitErr = cmd.Wait(); close(done) }()
	text := func(frame string) string {
		var rows []string
		for row := 1; row <= height; row++ {
			rows = append(rows, liveDiffFrameRow(frame, row))
		}
		return strings.Join(rows, "\n")
	}

	h.frame(t, func(frame string) bool {
		visible := text(frame)
		return strings.Contains(visible, "▶ Started") && !strings.Contains(visible, "Spawn assignment:") &&
			strings.Contains(visible, "Inspect parser.") && strings.Contains(visible, "Preserve behavior.")
	})
	// The parent is in a native wait: no root response is open, yet the pane updates.
	f.activity.collect("probe", "tool-1", "tool", "Read `live.go`")
	frame := h.frame(t, func(frame string) bool { return strings.Contains(text(frame), "Read live.go") })
	if row := liveDiffFrameRow(frame, 1); !strings.Contains(row, "AGENTS  3 agents") {
		t.Fatalf("header = %q", row)
	}
	f.activity.collect("probe", "tool-call\x00run-1", "tool", "Run `false`")
	f.activity.collect("probe", "tool-exit\x00run-1", "exit", "1")
	h.frame(t, func(frame string) bool { return strings.Contains(text(frame), "false (exit 1)") })
	f.activity.collect("probe", "tool-call\x00filtered-run", "tool", "Run `rg needle`")
	filtered := exploreFilterEvent{Command: "rg needle", LinesBefore: 49, LinesRemoved: 24, ElapsedMS: 627,
		Tokens: &exploreTokenReduction{Before: 1200, After: 700, Saved: 500, Percent: 41.7, Basis: "o200k_base"}}
	f.activity.collectEvent(activityEvent{thread: "probe", source: "filtered-run", kind: "output_filter", callID: "filtered-run", text: filtered.text(), filter: &filtered})
	frame = h.frame(t, func(frame string) bool {
		visible := text(frame)
		return strings.Contains(visible, "rg needle") && strings.Contains(visible, "~tokens 1.2K→700 (-41.7%)") && strings.Contains(visible, "−24/49 lines")
	})
	commandColumn, summaryColumn := -1, -1
	for row := 1; row <= height; row++ {
		line := liveDiffFrameRow(frame, row)
		if strings.Contains(line, "Run    rg needle") {
			commandColumn = strings.Index(line, "rg needle")
		}
		if strings.Contains(line, "~tokens") {
			summaryColumn = strings.Index(line, "~tokens")
		}
	}
	if commandColumn < 0 || commandColumn != summaryColumn {
		t.Fatalf("summary not aligned: command=%d summary=%d\n%s", commandColumn, summaryColumn, text(frame))
	}

	f.activity.collect("probe", "compact-1", "compaction", "Context compacted")
	h.frame(t, func(frame string) bool { return strings.Count(text(frame), "Context compacted") >= 2 })
	f.activity.collect("probe", "link-1", "commentary", "See [live.go](/tmp/live.go:4)")
	h.frame(t, func(frame string) bool {
		return strings.Contains(text(frame), "See live.go") && !strings.Contains(text(frame), "(/tmp/live.go:4)")
	})
	f.activity.collect("probe", "pane-cleanup", "tool", toolActivityShell("inspect_file --json --max-tokens 500 pane.go")+
		"\n\nEdit `pane.go` +1 -1\n```diff\n-pane-old-secret\n+pane-new-secret\n```")
	frame = h.frame(t, func(frame string) bool {
		visible := text(frame)
		return strings.Contains(visible, "Inspect pane.go") && strings.Contains(visible, "Edit pane.go")
	})
	if visible := text(frame); strings.Contains(visible, "pane-old-secret") || strings.Contains(visible, "pane-new-secret") {
		t.Fatalf("pane retained omitted tool details: %s", visible)
	}
	f.activity.collect("probe", "reply-in", "reply", "[`/root` -> `/root/explorer/probe`] Message received:\nCheck delivery.")
	h.frame(t, func(frame string) bool {
		return strings.Contains(text(frame), "✉  from main") && strings.Contains(text(frame), "Check delivery.")
	})
	f.activity.collect("probe", "reply-out", "reply", "[`/root/explorer/probe` -> `/root`] Message received:\nDelivery checked.")
	h.frame(t, func(frame string) bool {
		return strings.Contains(text(frame), "✉  to main") && strings.Contains(text(frame), "Delivery checked.")
	})
	h.write(t, "\x1b[<35;5;4M")
	h.frame(t, func(frame string) bool {
		return strings.Contains(frame, "\x1b[4mprobe\x1b[24m")
	})
	h.write(t, "\x1b[<0;5;4M")
	h.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 1), "only explorer/probe") && !strings.Contains(text(frame), "● explorer ─")
	})
	h.write(t, "\x1b[<0;5;4M")
	h.frame(t, func(frame string) bool { return strings.Contains(liveDiffFrameRow(frame, 1), "AGENTS  3 agents") })
	h.write(t, "o")
	h.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 1), "only explorer/probe") && strings.HasPrefix(liveDiffFrameRow(frame, height), "ONLY") &&
			!strings.Contains(text(frame), "● explorer ─")
	})
	h.quit(t)
	// A closed pane stays closed; the next root drain carries the backlog inline.
	waitActivityPaneState(t, f, activityPaneReleased)
	f.activity.collect("probe", "tool-2", "tool", "Read `after.go`")
	if got := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes)); !strings.Contains(got, "after.go") {
		t.Fatalf("drain after close = %q", got)
	}
}

func TestActivityPaneRestoreKeepsOriginalEvents(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	directed := "[`/root/explorer` -> `/root`] Message received:\nhello"
	f.activity.collect("explorer", "reply-1", "reply", directed)
	f.activity.collect("explorer", "op-1", "operation", "Reading old")
	generation, _, ok := f.activity.subscribePane()
	if !ok {
		t.Fatal("pane did not attach")
	}
	entries, _, _ := f.activity.takePane(generation)
	f.activity.collect("explorer", "op-2", "operation", "Reading new")
	f.activity.restorePane(entries)
	f.activity.releasePane()
	got := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes))
	if strings.Count(got, directed) != 1 || strings.Contains(got, "[`/root/explorer`] [`/root/explorer` ->") {
		t.Fatalf("restored directed text = %q", got)
	}
	if strings.Contains(got, "Reading old") || !strings.Contains(got, "Reading new") {
		t.Fatalf("restored a replaced operation: %q", got)
	}
}

func TestActivityPaneStreamsEscapedEntriesWithinLineLimit(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	// JSON escapes each of these bytes as <, so the raw length undercounts.
	text := strings.Repeat("<", maxCommentaryPublicationBytes-64)
	for i := range 40 {
		f.activity.collect("explorer", fmt.Sprintf("reply-%d", i), "reply", fmt.Sprintf("%d %s", i, text))
	}
	client := f.connect(t)
	client.scanner.Buffer(make([]byte, 4096), maxLiveDiffEventBytes+1)
	client.next(t, "snapshot")
	received := 0
	for received < 40 {
		entries := client.next(t, "entries").Entries
		if len(entries) != 1 {
			t.Fatalf("event carried %d entries, want one bounded entry", len(entries))
		}
		received += len(entries)
	}
	if received != 40 {
		t.Fatalf("received %d entries", received)
	}
}

func TestActivityPaneRetryDoesNotRepeatDivertedReply(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	f.activity.collect("explorer", "start", "start", "Started")
	message := assistantCommentaryMessage("reply-1", "[`/root/explorer` -> `/root`] Message received:\nhello")
	f.activity.divertRootReplies("root", []map[string]json.RawMessage{message}, []string{"/root/explorer"})
	f.activity.releasePane()
	if kept := f.activity.divertRootReplies("root", []map[string]json.RawMessage{message}, []string{"/root/explorer"}); len(kept) != 0 {
		t.Fatal("retry repeated a diverted reply inline")
	}
	if got := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes)); strings.Count(got, "hello") != 1 {
		t.Fatalf("diverted reply delivered %d times: %q", strings.Count(got, "hello"), got)
	}
}

func TestLiveActivityViewSummarizesCodeModeBatches(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	batch := "Read `a.go`\n\nRun `go test`\n```bash\ngo test ./...\n\necho done\n```\n\nRead `c.go`\n\nRun JavaScript · other code"
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 3, Agent: "/root/a", Kind: "tool", Text: batch, Observed: time.Now()}}})
	lines := plainLines(view.render(80, 20, time.Now()))
	// The roster shows the latest operation of the batch.
	if !strings.Contains(lines[1], "Run JavaScript · other code · +3 more") {
		t.Fatalf("batch roster row = %q", lines[1])
	}
	if strings.Contains(lines[2], "more") {
		t.Fatalf("single operation marked as a batch: %q", lines[2])
	}
}
