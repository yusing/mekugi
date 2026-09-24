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
	f.pane = newActivityPane(t.Context(), func() bool { f.launches++; return available })
	f.activity.attachPane(f.pane)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+liveActivityEventsPath, f.activity.serveActivityPane)
	mux.HandleFunc("POST "+liveActivityEventsPath, f.activity.serveActivitySelection)
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
	return f.connectView(t, "")
}

// connectView opens a stream with an endpoint query, such as "?view=roster".
func (f *activityPaneFixture) connectView(t *testing.T, query string) *activityPaneClient {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	connection := f.activity.paneDescriptor()
	connection.Endpoint += query
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
	if len(snapshot.Agents) != 2 || snapshot.Agents[0].Name != "/root/explorer" || snapshot.Agents[1].Name != "/root/explorer/probe" {
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
	if !state.Agents[1].Responding {
		t.Fatalf("responding not reported: %+v", state.Agents)
	}
	// Streamed deltas show an estimate on the next tick, without a wake.
	f.activity.streamOutput("probe", 400)
	estimate := client.next(t, "agents")
	for estimate.Agents[1].OutputTokens != 400/activityBytesPerToken {
		estimate = client.next(t, "agents")
	}
	// Usage replaces the estimate, accumulates per child, and reaches the
	// roster without new entries.
	f.activity.addUsage("probe", tokenCounts{InputTokens: 1200, OutputTokens: 30})
	f.activity.addUsage("probe", tokenCounts{InputTokens: 800, OutputTokens: 20})
	usage := client.next(t, "agents")
	for usage.Agents[1].InputTokens != 2000 {
		usage = client.next(t, "agents")
	}
	if usage.Agents[1].OutputTokens != 50 || usage.Agents[0].InputTokens != 0 {
		t.Fatalf("usage roster = %+v", usage.Agents)
	}
	f.activity.endResponse("probe")
	f.activity.markFinal("explorer", subagentFinal{sender: "/root/explorer/probe"})
	final := client.next(t, "agents")
	for final.Agents[1].Responding || !final.Agents[1].Final {
		final = client.next(t, "agents")
	}

	// Disconnect: within the grace window events wait for a reconnect.
	client.cancel()
	waitActivityPaneState(t, f, activityPaneClaimed)
	f.activity.collect("explorer", "tool-2", "tool", "Search `needle`")
	if got := f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes); len(got) != 0 {
		t.Fatalf("grace window leaked root copies: %s", drainText(got))
	}
	reconnect := f.connect(t)
	restored := reconnect.next(t, "snapshot")
	if len(restored.Entries) != 2 || restored.Entries[0].Text != start {
		t.Fatalf("reconnect history = %+v", restored.Entries)
	}
	pending := reconnect.next(t, "entries")
	if len(pending.Entries) != 1 || pending.Entries[0].Text != "Search `needle`" || pending.Entries[0].Seq <= restored.Entries[1].Seq {
		t.Fatalf("pending after reconnect = %+v", pending.Entries)
	}

	// After the grace window expires, delivery returns inline with one notice.
	reconnect.cancel()
	waitActivityPaneState(t, f, activityPaneClaimed)
	f.activity.collect("probe", "tool-3", "tool", "Read `b.go`")
	f.activity.mu.Lock()
	f.pane.deadline = time.Now().Add(-time.Second)
	f.activity.mu.Unlock()
	inline := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes))
	if !strings.Contains(inline, "pane closed") || !strings.Contains(inline, "/root/explorer/probe") || !strings.Contains(inline, "b.go") {
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

func TestActivityPaneRosterObservesAndSharesSelection(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	f.activity.collect("explorer", "start", "start", "Started")
	feed := f.connect(t)
	if snapshot := feed.next(t, "snapshot"); snapshot.Roster {
		t.Fatalf("feed saw a roster before one connected: %+v", snapshot)
	}
	roster := f.connectView(t, "?view=roster")
	if snapshot := roster.next(t, "snapshot"); len(snapshot.Agents) != 2 {
		t.Fatalf("roster snapshot = %+v", snapshot)
	}
	// The feed learns that a roster pane shows the agents.
	for event := feed.next(t, "agents", "entries"); !event.Roster; event = feed.next(t, "agents", "entries") {
	}

	// Delivered entries reach the roster too, without a second owner.
	f.activity.collect("probe", "tool-1", "tool", "Read `a.go`")
	delivered := feed.next(t, "entries")
	mirrored := roster.next(t, "entries")
	for !slices.ContainsFunc(mirrored.Entries, func(e activityPaneEntry) bool { return e.Text == "Read `a.go`" }) {
		mirrored = roster.next(t, "entries")
	}
	if last := delivered.Entries[len(delivered.Entries)-1]; last.Text != "Read `a.go`" || mirrored.Entries[len(mirrored.Entries)-1].Seq != last.Seq {
		t.Fatalf("roster entries = %+v, feed = %+v", mirrored.Entries, delivered.Entries)
	}
	f.activity.mu.Lock()
	state := f.pane.state
	f.activity.mu.Unlock()
	if state != activityPaneAttached {
		t.Fatalf("roster changed ownership: %v", state)
	}

	// A roster click filters the feed, and every viewer hears the selection.
	post := func(selection activityPaneSelection) int {
		body, _ := json.Marshal(selection)
		req, err := liveDiffRequest(t.Context(), f.activity.paneDescriptor(), http.MethodPost, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := post(activityPaneSelection{Selected: "/root/explorer/probe", Only: true}); status != http.StatusNoContent {
		t.Fatalf("selection status = %d", status)
	}
	for _, client := range []*activityPaneClient{feed, roster} {
		if event := client.next(t, "select"); event.Selected != "/root/explorer/probe" || !event.Only {
			t.Fatalf("select event = %+v", event)
		}
	}
	// A reconnecting roster restores the shared selection.
	roster.cancel()
	for event := feed.next(t, "agents"); event.Roster; event = feed.next(t, "agents") {
	}
	again := f.connectView(t, "?view=roster")
	if snapshot := again.next(t, "snapshot"); snapshot.Selected != "/root/explorer/probe" || !snapshot.Only {
		t.Fatalf("roster snapshot selection = %+v", snapshot)
	}

	// Once activity returns inline, the roster ends and cannot reconnect.
	feed.cancel()
	waitActivityPaneState(t, f, activityPaneClaimed)
	f.activity.mu.Lock()
	f.pane.deadline = time.Now().Add(-time.Second)
	f.activity.mu.Unlock()
	again.next(t, "end")
	if status := post(activityPaneSelection{Selected: "/root/explorer"}); status != http.StatusGone {
		t.Fatalf("released selection status = %d", status)
	}
	req, _ := liveDiffRequest(t.Context(), liveDiffConnection{Endpoint: f.activity.paneDescriptor().Endpoint + "?view=roster", Token: f.activity.paneDescriptor().Token}, http.MethodGet, nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusGone {
		t.Fatalf("released pane accepted a roster: %d", response.StatusCode)
	}
}

func TestActivityPaneShowsFinalAnswerWithoutRootCopy(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	final := subagentFinal{sender: "/root/explorer/probe", source: "answer-1", text: "The final result is ready.\n\n- verified"}
	f.activity.markFinal("explorer", final)
	f.activity.markFinal("explorer", final) // Replayed input must not duplicate the pane event.
	client := f.connect(t)
	snapshot := client.next(t, "snapshot")
	if !snapshot.Agents[1].Final {
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
	if !strings.Contains(frame, "✓ The final result is ready") || !strings.Contains(frame, "verified") {
		t.Fatalf("final content absent from pane: %s", frame)
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
	entries := client.next(t, "entries")
	if len(entries.Entries) != 2 || !strings.HasPrefix(entries.Entries[1].Text, "[`/root/explorer` -> `/root`]") {
		t.Fatalf("diverted reply = %+v", entries.Entries)
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
	if !strings.HasPrefix(lines[0], "AGENTS · 7 · 1 responding") || !strings.HasSuffix(lines[0], "FOLLOW") {
		t.Fatalf("header = %q", lines[0])
	}
	// body=18 → roster limit 6: five agents plus the overflow line. Each row
	// carries the agent's current operation.
	if !strings.HasPrefix(lines[1], "▸◐ a    Read a.go") || !strings.HasPrefix(lines[2], " · └ x  Read x.go") ||
		!strings.HasPrefix(lines[4], " ✓ b") || !strings.HasPrefix(lines[5], " ! c    ✗ failed") || lines[6] != "  +2 more · n/p" {
		t.Fatalf("roster = %q", lines[1:7])
	}
	for range 6 {
		view.selectAgent(1)
	}
	lines = plainLines(view.render(60, 20, time.Now()))
	if view.selected != "/root/e" || !strings.HasPrefix(lines[5], "▸· e") {
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
	if !strings.Contains(all, "… +11 lines · o") || !strings.Contains(all, "│ line") {
		t.Fatalf("clamped feed = %s", all)
	}
	view.handleKey("", 'o')
	only := plainLines(view.render(80, 40, time.Now()))
	joined := strings.Join(only, "\n")
	if strings.Contains(joined, "+11 lines") || strings.Count(joined, "│ line") != 20 || strings.Contains(joined, "● /root/b") {
		t.Fatalf("only feed = %s", joined)
	}
	if !strings.HasPrefix(only[len(only)-1], "ONLY ·") || !strings.Contains(only[0], "only /root/a (1/2)") {
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

func TestLiveActivityRosterShowsTokensAtRightEdge(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 146_800, 3_200
	for _, width := range []int{80, 140} {
		lines := plainLines(view.render(width, 20, time.Now()))
		row := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "↑") })
		// The roster status ends the row; cards end it at the column divider.
		status, _, _ := strings.Cut(lines[max(0, row)], " │ ")
		if row < 0 || !strings.HasSuffix(strings.TrimRight(status, " "), "now · ↑ 146.8K ↓ 3.2K") {
			t.Fatalf("width %d: tokens not right-aligned: %q", width, lines)
		}
		if slices.ContainsFunc(lines, func(line string) bool { return strings.Count(line, "↑") > 1 }) {
			t.Fatalf("width %d: agent without usage shows tokens: %q", width, lines)
		}
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
		return strings.Contains(visible, "▶ Started") && strings.Contains(visible, "Spawn assignment:") &&
			strings.Contains(visible, "Inspect parser.") && strings.Contains(visible, "Preserve behavior.")
	})
	// The parent is in a native wait: no root response is open, yet the pane updates.
	f.activity.collect("probe", "tool-1", "tool", "Read `live.go`")
	frame := h.frame(t, func(frame string) bool { return strings.Contains(text(frame), "Read live.go") })
	if row := liveDiffFrameRow(frame, 1); !strings.Contains(row, "AGENTS · 2") {
		t.Fatalf("header = %q", row)
	}
	f.activity.collect("probe", "tool-call\x00run-1", "tool", "Run `false`")
	f.activity.collect("probe", "tool-exit\x00run-1", "exit", "1")
	h.frame(t, func(frame string) bool { return strings.Contains(text(frame), "false (exit 1)") })
	f.activity.collect("probe", "compact-1", "compaction", "Context compacted")
	h.frame(t, func(frame string) bool { return strings.Count(text(frame), "Context compacted") >= 2 })
	f.activity.collect("probe", "link-1", "commentary", "See [live.go](/tmp/live.go:4)")
	h.frame(t, func(frame string) bool {
		return strings.Contains(text(frame), "See live.go") && !strings.Contains(text(frame), "(/tmp/live.go:4)")
	})
	h.write(t, "\x1b[<35;5;3M")
	h.frame(t, func(frame string) bool { return strings.Contains(frame, "\x1b[4m") })
	h.write(t, "\x1b[<0;5;3M")
	h.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 1), "only /root/explorer/probe") && !strings.Contains(text(frame), "● /root/explorer ─")
	})
	h.write(t, "\x1b[<0;5;3M")
	h.frame(t, func(frame string) bool { return strings.Contains(liveDiffFrameRow(frame, 1), "AGENTS · 2") })
	h.write(t, "o")
	h.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 1), "only /root/explorer/probe") && strings.HasPrefix(liveDiffFrameRow(frame, height), "ONLY") &&
			!strings.Contains(text(frame), "● /root/explorer ─")
	})
	h.quit(t)
	// A closed pane stays closed; the next root drain carries the backlog inline.
	waitActivityPaneState(t, f, activityPaneClaimed)
	f.activity.mu.Lock()
	f.pane.deadline = time.Now().Add(-time.Second)
	f.activity.mu.Unlock()
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
	f.activity.detachPane(generation)
	f.activity.releasePane()
	got := drainText(f.activity.drain("root", time.Now(), maxCommentaryPublicationBytes))
	if strings.Count(got, directed) != 1 || strings.Contains(got, "[`/root/explorer`] [`/root/explorer` ->") {
		t.Fatalf("restored directed text = %q", got)
	}
	if strings.Contains(got, "Reading old") || !strings.Contains(got, "Reading new") {
		t.Fatalf("restored a replaced operation: %q", got)
	}
}

func TestActivityPaneBatchesEscapedTextWithinLineLimit(t *testing.T) {
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
		received += len(client.next(t, "entries").Entries)
	}
	if received != 40 {
		t.Fatalf("received %d entries", received)
	}
	client.cancel()
	waitActivityPaneState(t, f, activityPaneClaimed)
	reconnect := f.connect(t)
	reconnect.scanner.Buffer(make([]byte, 4096), maxLiveDiffEventBytes+1)
	if history := reconnect.next(t, "snapshot").Entries; len(history) == 0 || history[len(history)-1].Text[:3] != "39 " {
		t.Fatalf("bounded snapshot lost the latest history: %d entries", len(history))
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

func TestLiveActivitySplitRosterAndFeedLayouts(t *testing.T) {
	// The roster pane under Codex shows the header and one row per agent.
	roster := liveActivityTestView("/root/a", "/root/b")
	roster.rosterPane = true
	lines := plainLines(roster.render(140, 6, time.Now()))
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "AGENTS · 2 · 0 responding") || strings.Contains(lines[0], "FOLLOW") {
		t.Fatalf("roster header = %q", lines)
	}
	if !strings.Contains(lines[1], "a  Read a.go") || !strings.Contains(lines[2], "b  Read b.go") || strings.Contains(strings.Join(lines, "\n"), "●") {
		t.Fatalf("roster pane rows = %q", lines)
	}
	// A click on a row selects that agent and shows only it.
	if !roster.handleMouse('\r', 3, 5) || roster.selection() != (activityPaneSelection{Selected: "/root/b", Only: true}) {
		t.Fatalf("roster click selection = %+v", roster.selection())
	}

	// With a roster pane connected, the feed fills its pane under one header.
	feed := liveActivityTestView("/root/a", "/root/b")
	feed.apply(activityPaneEvent{Kind: "agents", Agents: feed.agents, Roster: true})
	lines = plainLines(feed.render(140, 20, time.Now()))
	if !strings.HasPrefix(lines[0], "ACTIVITY · all agents") || !strings.HasSuffix(strings.TrimRight(lines[0], " "), "FOLLOW") ||
		!strings.Contains(lines[1], "● /root/a") || strings.Contains(lines[1], "│") ||
		strings.Contains(lines[len(lines)-1], "click agent") {
		t.Fatalf("feed-only layout = %q", lines)
	}
	// The shared selection filters the feed; a heartbeat keeps the layout.
	feed.apply(activityPaneEvent{Kind: "select", Selected: "/root/b", Only: true})
	feed.apply(activityPaneEvent{Kind: "heartbeat"})
	lines = plainLines(feed.render(140, 20, time.Now()))
	if !strings.HasPrefix(lines[0], "ACTIVITY · only /root/b (2/2)") || strings.Contains(strings.Join(lines, "\n"), "/root/a") {
		t.Fatalf("shared selection = %q", lines)
	}
	// Without the roster pane, the feed brings its own roster back.
	feed.apply(activityPaneEvent{Kind: "agents", Agents: feed.agents})
	if lines = plainLines(feed.render(140, 20, time.Now())); !strings.HasPrefix(lines[0], "AGENTS") || !strings.Contains(lines[1], "│") {
		t.Fatalf("combined layout = %q", lines)
	}
}

func TestLiveActivityIgnoresOwnSelectionEchoes(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	var sent []activityPaneSelection
	view.publish = func(selection activityPaneSelection) { sent = append(sent, selection) }
	// Two quick local changes: the first echo must not undo the second.
	before := view.selection()
	view.handleKey("", 'o')
	view.publishSelection(before)
	before = view.selection()
	view.handleKey("", 'n')
	view.publishSelection(before)
	if len(sent) != 2 || view.selection() != (activityPaneSelection{Selected: "/root/b", Only: true}) {
		t.Fatalf("sent = %+v, selection = %+v", sent, view.selection())
	}
	view.apply(activityPaneEvent{Kind: "select", Selected: sent[0].Selected, Only: sent[0].Only})
	if view.selection() != sent[1] {
		t.Fatalf("own echo undid a later change: %+v", view.selection())
	}
	view.apply(activityPaneEvent{Kind: "select", Selected: sent[1].Selected, Only: sent[1].Only})
	// Another viewer's change still applies.
	view.apply(activityPaneEvent{Kind: "select", Selected: "/root/a"})
	if view.selection() != (activityPaneSelection{Selected: "/root/a"}) || len(view.pending) != 0 {
		t.Fatalf("foreign selection = %+v, pending = %+v", view.selection(), view.pending)
	}
}

func TestLiveActivityRosterProcess(t *testing.T) {
	if os.Getenv("MEKUGI_LIVE_ROSTER_TEST_CHILD") == "1" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		os.Exit(RunLiveActivity(ctx, []string{"--view", "roster", "--session-file", os.Getenv("MEKUGI_LIVE_ACTIVITY_SESSION")}, os.Stdin, os.Stdout, os.Stderr))
	}
	f := newActivityPaneFixture(t, true)
	f.activity.collect("explorer", "start", "start", "Started")
	f.activity.collect("probe", "tool-1", "tool", "Read `live.go`")
	feed := f.connect(t)
	feed.next(t, "snapshot")
	session := filepath.Join(t.TempDir(), "activity.json")
	data, err := json.Marshal(f.activity.paneDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, data, 0600); err != nil {
		t.Fatal(err)
	}
	const height = 6
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveActivityRosterProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_ROSTER_TEST_CHILD=1", "MEKUGI_LIVE_ACTIVITY_SESSION="+session)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: height, Cols: 120})
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
	h.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 1), "AGENTS · 2") && !strings.Contains(liveDiffFrameRow(frame, 1), "FOLLOW") &&
			strings.Contains(liveDiffFrameRow(frame, 3), "Read live.go")
	})
	// The feed hears the roster, then its click on the probe row.
	for event := feed.next(t, "agents", "entries"); !event.Roster; event = feed.next(t, "agents", "entries") {
	}
	h.write(t, "\x1b[<0;5;3M")
	if event := feed.next(t, "select"); event.Selected != "/root/explorer/probe" || !event.Only {
		t.Fatalf("roster click = %+v", event)
	}
	h.frame(t, func(frame string) bool {
		return strings.Contains(liveDiffFrameRow(frame, 1), "only /root/explorer/probe")
	})
	// Arrow keys move the shown agent, and the feed follows.
	h.write(t, "\x1b[A")
	if event := feed.next(t, "select"); event.Selected != "/root/explorer" || !event.Only {
		t.Fatalf("roster up arrow = %+v", event)
	}
	h.write(t, "\x1b[A")
	if event := feed.next(t, "select"); event.Only {
		t.Fatalf("up from the first agent did not show all agents: %+v", event)
	}
	h.quit(t)
	for event := feed.next(t, "agents"); event.Roster; event = feed.next(t, "agents") {
	}
}

func TestLiveActivityRosterPaneArrowKeys(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	view.rosterPane = true
	keys := func(input string) {
		escape := ""
		for i := range len(input) {
			escape, _ = view.handleKey(escape, input[i])
		}
	}
	lines := plainLines(view.render(120, 5, time.Now()))
	if !strings.HasSuffix(strings.TrimRight(lines[0], " "), "↓ show agent") || strings.Contains(strings.Join(lines, "\n"), "▸") {
		t.Fatalf("all-agents roster = %q", lines)
	}
	// "All agents" sits above the first agent, and moves stop at either end.
	for _, step := range []struct {
		input string
		want  activityPaneSelection
	}{
		{"\x1b[B", activityPaneSelection{Selected: "/root/a", Only: true}},
		{"\x1bOB", activityPaneSelection{Selected: "/root/b", Only: true}},
		{"j", activityPaneSelection{Selected: "/root/b", Only: true}},
		{"\x1b[A", activityPaneSelection{Selected: "/root/a", Only: true}},
		{"k", activityPaneSelection{Selected: "/root/a"}},
		{"\x1b[A", activityPaneSelection{Selected: "/root/a"}},
		{"o", activityPaneSelection{Selected: "/root/a", Only: true}},
	} {
		keys(step.input)
		if got := view.selection(); got != step.want {
			t.Fatalf("after %q: selection = %+v, want %+v", step.input, got, step.want)
		}
	}
	lines = plainLines(view.render(120, 5, time.Now()))
	if !strings.HasPrefix(lines[1], "▸") || !strings.HasSuffix(strings.TrimRight(lines[0], " "), "↑/↓ agent · o all") {
		t.Fatalf("shown-agent roster = %q", lines)
	}

	// The feed pane leaves selection to a connected roster pane but still scrolls.
	feed := liveActivityTestView("/root/a", "/root/b")
	feed.apply(activityPaneEvent{Kind: "agents", Agents: feed.agents, Roster: true})
	before := feed.selection()
	for _, key := range []byte("np\to") {
		feed.handleKey("", key)
	}
	if feed.selection() != before {
		t.Fatalf("feed pane changed selection: %+v", feed.selection())
	}
	lines = plainLines(feed.render(120, 20, time.Now()))
	if footer := lines[len(lines)-1]; !strings.HasSuffix(footer, "j/k scroll · r follow") || strings.Contains(footer, "n/p") {
		t.Fatalf("feed footer = %q", footer)
	}
	feed.handleKey("", 'k')
	if feed.following {
		t.Fatal("feed pane no longer scrolls")
	}
	// Without a roster pane, the feed keeps its own selection keys.
	feed.apply(activityPaneEvent{Kind: "agents", Agents: feed.agents})
	feed.handleKey("", 'n')
	if feed.selection() == before {
		t.Fatal("combined feed lost its selection keys")
	}
}
