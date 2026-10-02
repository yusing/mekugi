package router

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func replayPlaybackTestSource() *sessionUIReplay {
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
	event := func(second int, method string, item appServerItem, delta string) uiReplayEvent {
		return uiReplayEvent{At: start.Add(time.Duration(second) * time.Second), Method: method, Params: appServerEvent{ThreadID: "root", TurnID: "turn", ItemID: item.ID, Item: item, Delta: delta}}
	}
	command := appServerItem{ID: "command", Type: "commandExecution", Command: "sleep 1; printf 'captured output'", Cwd: "/workspace/replay", Status: "inProgress"}
	completed := command
	completed.Status, completed.ExitCode, completed.DurationMS = "completed", new(0), new(int64(2000))
	completed.AggregatedOutput = new("captured output\n")
	message := appServerItem{ID: "answer", Type: "agentMessage", Phase: "final_answer"}
	answer := message
	answer.Text = "Repaint diagnosis complete."
	events := []uiReplayEvent{
		{At: start, Method: "turn/started", Params: appServerEvent{ThreadID: "root", TurnID: "turn", Turn: appServerTurn{ID: "turn", Status: "inProgress"}}},
		event(1, "replay/providerStarted", appServerItem{}, ""),
		event(2, "item/reasoning/summaryTextDelta", appServerItem{ID: "reasoning", Type: "reasoning"}, "Inspecting repaint timing."),
		event(3, "item/completed", appServerItem{ID: "reasoning", Type: "reasoning", Summary: []string{"Inspecting repaint timing."}}, ""),
		event(3, "replay/providerCompleted", appServerItem{}, ""),
		event(4, "item/started", command, ""),
		event(5, "item/commandExecution/outputDelta", command, "captured output\n"),
		event(6, "item/completed", completed, ""),
		event(7, "item/started", message, ""),
		event(8, "item/agentMessage/delta", message, "Repaint diagnosis "),
		event(9, "item/completed", answer, ""),
		{At: start.Add(10 * time.Second), Method: "turn/completed", Params: appServerEvent{ThreadID: "root", TurnID: "turn", Turn: appServerTurn{ID: "turn", Status: "completed"}}},
	}
	return &sessionUIReplay{Thread: "root", Cwd: "/workspace/replay", Start: start, End: start.Add(20 * time.Second), Threads: map[string]string{"root": "/root"}, Events: events, Items: 3, Providers: 1}
}

func replayPlaybackTestNew(t *testing.T) *uiReplayPlayback {
	t.Helper()
	p := newUIReplayPlayback(t.Context(), replayPlaybackTestSource(), 1)
	t.Cleanup(p.close)
	p.ui.view.painter.Theme, p.ui.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
	return p
}

func replayPlaybackTestPaint(t *testing.T, p *uiReplayPlayback, width, height int) string {
	t.Helper()
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	p.ui.shell.paintedRows = nil
	if err := p.paint(screen, width, height); err != nil {
		t.Fatal(err)
	}
	return screen.String()
}

func TestSessionUIReplayPlaybackUsesRecordedClockAndSeekReset(t *testing.T) {
	p := replayPlaybackTestNew(t)
	var outgoing bytes.Buffer
	p.ui.client.Input = replayDiscard{Writer: &outgoing}
	if err := p.advance(4500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	wantNow := p.source.Start.Add(4500 * time.Millisecond)
	if !p.at.Equal(wantNow) || !p.ui.now().Equal(wantNow) || !p.ui.view.clock().Equal(wantNow) || !p.ui.agents.clock().Equal(wantNow) {
		t.Fatal("presentation clocks are not tied to recorded position")
	}
	command := p.ui.session.commands[[3]string{"root", "turn", "command"}]
	if command == nil || !command.entry.Observed.Equal(p.source.Start.Add(4*time.Second)) || !command.entry.native.commandStarted.Equal(p.source.Start.Add(4*time.Second)) {
		t.Fatalf("command start used playback wall time: %+v", command)
	}
	if quit, err := p.key(' '); err != nil || quit || !p.paused {
		t.Fatalf("pause: quit=%v error=%v paused=%v", quit, err, p.paused)
	}
	next := p.next
	replayPlaybackTestPaint(t, p, 120, 28)
	replayPlaybackTestPaint(t, p, 120, 28)
	if p.position != 4500*time.Millisecond || p.next != next || !p.ui.now().Equal(wantNow) {
		t.Fatal("painting while paused advanced recorded time")
	}
	if err := p.advance(12 * time.Second); err != nil {
		t.Fatal(err)
	}
	replayPlaybackTestPaint(t, p, 120, 28)
	oldUI := p.ui
	if err := p.advance(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if p.ui == oldUI || len(p.ui.session.commands) != 0 || p.ui.turn != "turn" || !p.at.Equal(p.source.Start.Add(2*time.Second)) {
		t.Fatalf("seek did not rebuild earlier state: position=%v commands=%d", p.position, len(p.ui.session.commands))
	}
	if p.ui.session.reasoning[[3]string{"root", "turn", "reasoning"}] != "Inspecting repaint timing." {
		t.Fatal("seek did not replay earlier reasoning")
	}
	for _, entry := range p.ui.view.entries {
		if strings.Contains(entry.Text, "captured output") || strings.Contains(entry.Text, "Repaint diagnosis") {
			t.Fatalf("future content survived rewind: %+v", entry)
		}
	}
	p.ui.client.Input = replayDiscard{Writer: &outgoing}
	for _, key := range []byte{'\n', 'a', '/', '?', 'j', 'k', 'r'} {
		if quit, err := p.key(key); err != nil || quit {
			t.Fatalf("replay key %q: quit=%v error=%v", key, quit, err)
		}
	}
	if p.paused || p.position != p.from || p.ui.draft != "" || outgoing.Len() != 0 {
		t.Fatalf("replay controls leaked into live composer/transport: paused=%v position=%v draft=%q writes=%d", p.paused, p.position, p.ui.draft, outgoing.Len())
	}
	if p.ui.proxy != nil || p.ui.journal != nil || p.ui.execTrack != nil || p.ui.session.waitStore != nil {
		t.Fatal("offline playback constructed live execution dependencies")
	}
}

func TestSessionUIReplayPlaybackShowsOnlyArrivedContent(t *testing.T) {
	p := replayPlaybackTestNew(t)
	if err := p.advance(4500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	early := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if !strings.Contains(early, "printf") || strings.Contains(early, "Repaint diagnosis complete.") {
		t.Fatalf("early frame lost running command or exposed final answer:\n%s", early)
	}
	if run := p.ui.session.commands[[3]string{"root", "turn", "command"}]; run == nil || run.done != nil || run.completedAt != (time.Time{}) {
		t.Fatal("running command was prematurely completed")
	}
	if err := p.advance(8 * time.Second); err != nil {
		t.Fatal(err)
	}
	partial := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if !strings.Contains(partial, "Repaint diagnosis") || strings.Contains(partial, "Repaint diagnosis complete.") {
		t.Fatalf("streaming frame does not reflect arrived message prefix:\n%s", partial)
	}
	if err := p.advance(12 * time.Second); err != nil {
		t.Fatal(err)
	}
	final := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if !strings.Contains(final, "Repaint diagnosis complete.") || len(p.ui.session.commands) != 0 {
		t.Fatalf("completed frame/state did not settle:\n%s", final)
	}
}

func TestSessionUIReplayPlaybackControlsClampAndPreserveClock(t *testing.T) {
	p := replayPlaybackTestNew(t)
	p.from = 2 * time.Second
	if err := p.advance(p.from); err != nil {
		t.Fatal(err)
	}
	for _, key := range []byte{'+', '+', '-'} {
		if _, err := p.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if p.speed != 2 || p.position != p.from || !p.at.Equal(p.source.Start.Add(p.from)) {
		t.Fatal("speed changes rewrote recorded position")
	}
	for range 12 {
		p.key('+')
	}
	if p.speed != 100 {
		t.Fatalf("speed upper limit = %v", p.speed)
	}
	for range 20 {
		p.key('-')
	}
	if p.speed != 0.1 {
		t.Fatalf("speed lower limit = %v", p.speed)
	}
	if _, err := p.key(']'); err != nil || p.position != 12*time.Second {
		t.Fatalf("forward seek: position=%v error=%v", p.position, err)
	}
	if _, err := p.key(']'); err != nil || p.position != p.until {
		t.Fatalf("forward seek limit: position=%v error=%v", p.position, err)
	}
	if _, err := p.key('['); err != nil || p.position != 10*time.Second {
		t.Fatalf("backward seek: position=%v error=%v", p.position, err)
	}
	if _, err := p.key('['); err != nil || p.position != p.from {
		t.Fatalf("backward seek start limit: position=%v error=%v", p.position, err)
	}
	for _, key := range []byte{'q', 3, 27} {
		if quit, err := p.key(key); err != nil || !quit {
			t.Fatalf("quit key %q: quit=%v error=%v", key, quit, err)
		}
	}
}

func TestSessionUIReplayBatchElapsedUsesRecordedClock(t *testing.T) {
	p := replayPlaybackTestNew(t)
	for _, tc := range []struct {
		position time.Duration
		elapsed  string
	}{
		{4500 * time.Millisecond, "500ms"},
		{5500 * time.Millisecond, "1s"},
		{4500 * time.Millisecond, "500ms"},
	} {
		move := p.advance
		if tc.position < p.position {
			move = p.seek
		}
		if err := move(tc.position); err != nil {
			t.Fatal(err)
		}
		frame := replayPlaybackTestPaint(t, p, 120, 28)
		if !strings.Contains(frame, "Running shell batch · "+tc.elapsed) {
			t.Fatalf("replayed elapsed time at %v is not %s:\n%s", tc.position, tc.elapsed, frame)
		}
	}
}

func TestUISnapshotSessionUIReplay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		position time.Duration
		paused   bool
	}{
		{"running", 4500 * time.Millisecond, false},
		{"paused-streaming", 8 * time.Second, true},
		{"completed", 20 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := replayPlaybackTestNew(t)
			if err := p.advance(tc.position); err != nil {
				t.Fatal(err)
			}
			p.paused = tc.paused
			uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-"+tc.name+".txt", replayPlaybackTestPaint(t, p, 120, 28))
		})
	}
	t.Run("narrow-paused-bar", func(t *testing.T) {
		p := replayPlaybackTestNew(t)
		if err := p.advance(8 * time.Second); err != nil {
			t.Fatal(err)
		}
		p.paused = true
		uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-narrow-paused-bar.txt", p.bar(42)+"\n")
	})
}
