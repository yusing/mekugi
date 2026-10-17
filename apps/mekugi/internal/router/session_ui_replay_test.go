package router

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func replayPlaybackTestSource() *playback.Source {
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
	event := func(second int, method string, item observation.Item, delta string) playback.Event {
		return playback.Event{Event: observation.Event{At: start.Add(time.Duration(second) * time.Second), Kind: observation.Kind(method), ThreadID: "root", TurnID: "turn", ItemID: item.ID, Item: item, Delta: delta}}
	}
	command := observation.Item{ID: "command", Type: "commandExecution", Command: "sleep 1; printf 'captured output'", Cwd: "/workspace/replay", Status: "inProgress"}
	completed := command
	completed.Status, completed.ExitCode, completed.DurationMS = "completed", new(0), new(int64(2000))
	completed.AggregatedOutput = new("captured output\n")
	message := observation.Item{ID: "answer", Type: "agentMessage", Phase: "final_answer"}
	answer := message
	answer.Text = "Repaint diagnosis complete."
	events := []playback.Event{
		{Event: observation.Event{At: start, Kind: "turn/started", ThreadID: "root", TurnID: "turn", TurnStatus: "inProgress"}},
		event(1, "replay/providerStarted", observation.Item{}, ""),
		event(2, "item/reasoning/summaryTextDelta", observation.Item{ID: "reasoning", Type: "reasoning"}, "Inspecting repaint timing."),
		event(3, "item/completed", observation.Item{ID: "reasoning", Type: "reasoning", Summary: []string{"Inspecting repaint timing."}}, ""),
		event(3, "replay/providerCompleted", observation.Item{}, ""),
		event(4, "item/started", command, ""),
		event(5, "item/commandExecution/outputDelta", command, "captured output\n"),
		event(6, "item/completed", completed, ""),
		event(7, "item/started", message, ""),
		event(8, "item/agentMessage/delta", message, "Repaint diagnosis "),
		event(9, "item/completed", answer, ""),
		{Event: observation.Event{At: start.Add(10 * time.Second), Kind: "turn/completed", ThreadID: "root", TurnID: "turn", TurnStatus: "completed"}},
	}
	return &playback.Source{Thread: "root", Cwd: "/workspace/replay", Start: start, End: start.Add(20 * time.Second), Threads: map[string]string{"root": "/root"}, Events: events, Items: 3, Providers: 1}
}

func replayPlaybackTestNew(t *testing.T) *uiReplayPlayback {
	t.Helper()
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), replayPlaybackTestSource(), 1)
	t.Cleanup(p.close)
	p.ui.view.Painter.Theme, p.ui.agents.Painter.Theme = diffrender.DarkTheme, diffrender.DarkTheme
	return p
}

func replayPlaybackTestPaint(t *testing.T, p *uiReplayPlayback, width, height int) string {
	t.Helper()
	if p.ui.client != nil || p.ui.requests != nil || len(p.ui.session.metadata) != 0 || (p.ui.controlObservations != nil || p.ui.usage != nil || p.ui.audience != nil || p.ui.retained.Store != nil || p.ui.execWindows != nil || p.ui.nativeTrace != nil || p.ui.guard != nil) || p.ui.execTrack != nil || p.ui.session.waitStore != nil {
		t.Fatal("playback constructed live execution dependencies")
	}
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	p.ui.shell.frame.Rows = nil
	if err := p.paint(screen, width, height); err != nil {
		t.Fatal(err)
	}
	return screen.String()
}

func TestSessionUIReplayPlaybackUsesRecordedClockAndSeekReset(t *testing.T) {
	t.Parallel()
	p := replayPlaybackTestNew(t)
	if err := p.advance(4500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	wantNow := p.Source.Start.Add(4500 * time.Millisecond)
	if !p.At.Equal(wantNow) || !p.ui.now().Equal(wantNow) || !p.ui.view.Clock().Equal(wantNow) || !p.ui.agents.Clock().Equal(wantNow) {
		t.Fatal("presentation clocks are not tied to recorded position")
	}
	command := p.ui.session.commands[[3]string{"root", "turn", "command"}]
	if command == nil || !command.entry.Observed.Equal(p.Source.Start.Add(4*time.Second)) || !command.entry.native.commandStarted.Equal(p.Source.Start.Add(4*time.Second)) {
		t.Fatalf("command start used playback wall time: %+v", command)
	}
	if quit, err := p.key(' '); err != nil || quit || !p.Paused {
		t.Fatalf("pause: quit=%v error=%v paused=%v", quit, err, p.Paused)
	}
	next := p.Next
	replayPlaybackTestPaint(t, p, 120, 28)
	replayPlaybackTestPaint(t, p, 120, 28)
	if p.Position != 4500*time.Millisecond || p.Next != next || !p.ui.now().Equal(wantNow) {
		t.Fatal("painting while paused advanced recorded time")
	}
	if err := p.advance(12 * time.Second); err != nil {
		t.Fatal(err)
	}
	replayPlaybackTestPaint(t, p, 120, 28)
	oldUI := p.ui
	oldThread := p.ui.session.FindThread(p.Source.Thread)
	outputs := p.activity.Outputs(p.ui.session.Scope())
	if err := p.advance(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	if p.activity.Outputs(p.ui.session.Scope()) == outputs {
		t.Fatal("backward seek reused activity state")
	}
	if p.ui.session.FindThread(p.Source.Thread) == oldThread || oldUI.session.Scope() != nil || oldUI.session.Transcript(p.Source.Thread, "turn").Turn != nil {
		t.Fatal("backward seek retained old correlation scopes")
	}
	if p.ui == oldUI || len(p.ui.session.commands) != 0 || p.ui.Turn != "turn" || !p.At.Equal(p.Source.Start.Add(2*time.Second)) {
		t.Fatalf("seek did not rebuild earlier state: position=%v commands=%d", p.Position, len(p.ui.session.commands))
	}
	if !strings.Contains(replayPlaybackTestPaint(t, p, 120, 28), "Inspecting repaint timing.") {
		t.Fatal("seek did not replay earlier reasoning")
	}
	for _, entry := range p.ui.view.Entries {
		if strings.Contains(entry.Source.Text, "captured output") || strings.Contains(entry.Source.Text, "Repaint diagnosis") {
			t.Fatalf("future content survived rewind: %+v", entry)
		}
	}
	for _, key := range []byte{'\n', 'a', '/', '?', 'j', 'k', 'r'} {
		if quit, err := p.key(key); err != nil || quit {
			t.Fatalf("replay key %q: quit=%v error=%v", key, quit, err)
		}
	}
	if p.Paused || p.Position != p.From || p.ui.Text != "" {
		t.Fatalf("replay controls leaked into live composer/transport: paused=%v position=%v draft=%q", p.Paused, p.Position, p.ui.Text)
	}
	if p.ui.client != nil || p.ui.requests != nil || len(p.ui.session.metadata) != 0 || (p.ui.controlObservations != nil || p.ui.usage != nil || p.ui.audience != nil || p.ui.retained.Store != nil || p.ui.execWindows != nil || p.ui.nativeTrace != nil || p.ui.guard != nil) || p.ui.execTrack != nil || p.ui.session.waitStore != nil {
		t.Fatal("offline playback constructed live execution dependencies")
	}
}

func TestSessionUIReplayPlaybackShowsOnlyArrivedContent(t *testing.T) {
	t.Parallel()
	p := replayPlaybackTestNew(t)
	if err := p.advance(4500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	early := ansi.Strip(replayPlaybackTestPaint(t, p, 120, 28))
	if !strings.Contains(early, "printf") || strings.Contains(early, "Repaint diagnosis complete.") {
		t.Fatalf("early frame lost running command or exposed final answer:\n%s", early)
	}
	if run := p.ui.session.commands[[3]string{"root", "turn", "command"}]; run == nil || run.holding || run.completedAt != (time.Time{}) {
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
	t.Parallel()
	p := replayPlaybackTestNew(t)
	p.From = 2 * time.Second
	if err := p.advance(p.From); err != nil {
		t.Fatal(err)
	}
	for _, key := range []byte{'+', '+', '-'} {
		if _, err := p.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if p.Speed != 2 || p.Position != p.From || !p.At.Equal(p.Source.Start.Add(p.From)) {
		t.Fatal("speed changes rewrote recorded position")
	}
	for range 12 {
		p.key('+')
	}
	if p.Speed != 100 {
		t.Fatalf("speed upper limit = %v", p.Speed)
	}
	for range 20 {
		p.key('-')
	}
	if p.Speed != 0.1 {
		t.Fatalf("speed lower limit = %v", p.Speed)
	}
	if _, err := p.key(']'); err != nil || p.Position != 12*time.Second {
		t.Fatalf("forward seek: position=%v error=%v", p.Position, err)
	}
	if _, err := p.key(']'); err != nil || p.Position != p.Until {
		t.Fatalf("forward seek limit: position=%v error=%v", p.Position, err)
	}
	if _, err := p.key('['); err != nil || p.Position != 10*time.Second {
		t.Fatalf("backward seek: position=%v error=%v", p.Position, err)
	}
	if _, err := p.key('['); err != nil || p.Position != p.From {
		t.Fatalf("backward seek start limit: position=%v error=%v", p.Position, err)
	}
	for _, key := range []byte{'q', 3, 27} {
		if quit, err := p.key(key); err != nil || !quit {
			t.Fatalf("quit key %q: quit=%v error=%v", key, quit, err)
		}
	}
}

func TestSessionUIReplayBatchElapsedUsesRecordedClock(t *testing.T) {
	t.Parallel()
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
		if tc.position < p.Position {
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
	t.Parallel()
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
			p.Paused = tc.paused
			uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-"+tc.name+".txt", replayPlaybackTestPaint(t, p, 120, 28))
		})
	}
	t.Run("narrow-paused-bar", func(t *testing.T) {
		p := replayPlaybackTestNew(t)
		if err := p.advance(8 * time.Second); err != nil {
			t.Fatal(err)
		}
		p.Paused = true
		uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-narrow-paused-bar.txt", p.bar(42)+"\n")
	})
}

func newTestUIReplayPlayback(activity *activitymodel.Service, ctx context.Context, source *playback.Source, speed float64) *uiReplayPlayback {
	conversation := new(conversationmodel.Service)
	observations := conversationbinding.Codex.Observation(conversation)
	reviewSource := new(changecapture.Service)
	return newUIReplayPlayback(testTerminalViews(reviewSource), reviewSource, testPresentation(), conversation, &observations, activity, ctx, source, speed)
}

func runTestSessionUIReplay(activity *activitymodel.Service, service *playback.Service, ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
	conversation := new(conversationmodel.Service)
	observations := conversationbinding.Codex.Observation(conversation)
	reviewSource := new(changecapture.Service)
	return RunSessionUIReplay(testTerminalViews(reviewSource), reviewSource, testPresentation(), conversation, &observations, activity, service, ctx, args, stdin, stdout, stderr)
}
