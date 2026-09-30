package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"github.com/yusing/mekugi/internal/uisnapshot"
	"golang.org/x/term"
)

func TestSessionUIReplaySpeedPresetRoundTrips(t *testing.T) {
	steps := []float64{0.1, 0.125, 0.25, 0.5, 1, 2, 4, 8, 16, 32, 64, 100}
	p := replayPlaybackTestNew(t)
	if err := p.advance(4500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	p.paused = true
	position, at, next := p.position, p.at, p.next
	press := func(key byte, want float64) {
		t.Helper()
		quit, err := p.key(key)
		if err != nil || quit || p.speed != want {
			t.Fatalf("key %q: speed=%v want=%v quit=%v error=%v", key, p.speed, want, quit, err)
		}
		if p.position != position || !p.at.Equal(at) || p.next != next || !p.paused || !p.ui.now().Equal(at) {
			t.Fatal("speed control changed recorded clock, event cursor, or pause state")
		}
	}
	press('+', 2)
	press('-', 1)
	press('-', 0.5)
	press('+', 1)
	for _, want := range steps[5:] {
		press('+', want)
	}
	press('+', 100)
	for i := len(steps) - 2; i >= 0; i-- {
		press('-', steps[i])
	}
	press('-', 0.1)
	for _, want := range steps[1:5] {
		press('+', want)
	}
	press('=', 2)
	press('-', 1)
}

func TestSessionUIReplayArbitrarySpeedUsesDirectionalPreset(t *testing.T) {
	for _, tc := range []struct {
		speed, slower, faster float64
	}{
		{0.11, 0.1, 0.125},
		{0.2, 0.125, 0.25},
		{0.8, 0.5, 1},
		{1.5, 1, 2},
		{3, 2, 4},
		{99, 64, 100},
	} {
		p := replayPlaybackTestNew(t)
		for _, direction := range []struct {
			key  byte
			want float64
		}{{'-', tc.slower}, {'+', tc.faster}} {
			p.speed = tc.speed
			if quit, err := p.key(direction.key); err != nil || quit || p.speed != direction.want {
				t.Fatalf("speed=%v key=%q: got=%v want=%v quit=%v error=%v", tc.speed, direction.key, p.speed, direction.want, quit, err)
			}
		}
	}
}

func TestUISnapshotSessionUIReplayFractionalSpeed(t *testing.T) {
	p := replayPlaybackTestNew(t)
	if err := p.advance(8 * time.Second); err != nil {
		t.Fatal(err)
	}
	p.speed, p.paused = 0.125, true
	uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-fractional-speed.txt", replayPlaybackTestPaint(t, p, 120, 28))
}

func TestSessionUIReplayInputReportsDoNotBecomeControls(t *testing.T) {
	for _, tc := range []struct {
		name, report string
		want         byte
	}{
		{"wheel-up", "\x1b[<64;10;5M", terminalui.PaneWheelUp},
		{"wheel-down", "\x1b[<65;10;5M", terminalui.PaneWheelDown},
		{"modified-wheel", "\x1b[<92;10;5M", terminalui.PaneWheelUp},
		{"click", "\x1b[<0;10;5M", 0},
		{"release", "\x1b[<64;10;5m", 0},
		{"unknown-button", "\x1b[<128;10;5M", 0},
		{"malformed-control-bytes", "\x1b[<q+-;10;5M", 0},
		{"missing-coordinate", "\x1b[<64;10M", 0},
		{"zero-coordinate", "\x1b[<64;0;5M", 0},
		{"overlong", "\x1b[<" + strings.Repeat("1", 60) + ";10;5M", 0},
		{"arrow", "\x1b[A", 0},
		{"modified-arrow", "\x1b[1;5A", 0},
		{"ss3", "\x1bOP", 0},
		{"x10-control-payload", "\x1b[Mq+-", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input uiReplayInput
			t.Cleanup(input.close)
			p := replayPlaybackTestNew(t)
			p.paused = true
			var actions []byte
			// Each byte is a separate consume call, covering reports split
			// at every read boundary without letting ESC reach playback.key.
			for i := range len(tc.report) {
				key := input.consume(tc.report[i])
				if key != 0 {
					actions = append(actions, key)
					if quit, err := p.key(key); err != nil || quit {
						t.Fatalf("report byte %d quit playback: quit=%v error=%v", i, quit, err)
					}
				}
			}
			want := []byte(nil)
			if tc.want != 0 {
				want = []byte{tc.want}
			}
			if !reflect.DeepEqual(actions, want) || input.escapeC != nil {
				t.Fatalf("actions=%v want=%v escape timeout still armed=%v", actions, want, input.escapeC != nil)
			}
			if p.speed != 1 || !p.paused || p.position != 0 {
				t.Fatal("terminal report payload changed playback state")
			}
			if got := input.consume('+'); got != '+' {
				t.Fatalf("ordinary input after report = %q", got)
			}
		})
	}
}

func TestSessionUIReplayInputBareEscapeTimeout(t *testing.T) {
	var input uiReplayInput
	defer input.close()
	started := time.Now()
	if got := input.consume(27); got != 0 || input.escapeC == nil {
		t.Fatalf("bare Escape dispatched before disambiguation: key=%d timer=%v", got, input.escapeC)
	}
	select {
	case <-input.escapeC:
		if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
			t.Fatalf("Escape expired before 40ms: %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("standalone Escape did not become a quit timeout")
	}
}

func TestSessionUIReplayInputInterruptedReportCanQuit(t *testing.T) {
	for _, partial := range []string{"\x1b[", "\x1b[<64;", "\x1b[Mq"} {
		var input uiReplayInput
		for i := range len(partial) {
			input.consume(partial[i])
		}
		if got := input.consume(3); got != 3 {
			t.Fatalf("Ctrl+C swallowed by incomplete %q", partial)
		}
		if got := input.consume(27); got != 0 || input.escapeC == nil || input.mouse.Active || input.x10 != 0 {
			t.Fatalf("Escape did not recover incomplete %q", partial)
		}
		input.close()
	}
}

func TestSessionUIReplayControlsAccountForElapsedTime(t *testing.T) {
	p := replayPlaybackTestNew(t)
	for _, event := range []struct {
		key     byte
		elapsed time.Duration
		want    time.Duration
	}{
		{terminalui.PaneWheelUp, 10 * time.Millisecond, 10 * time.Millisecond},
		{terminalui.PaneWheelDown, 10 * time.Millisecond, 20 * time.Millisecond},
		{terminalui.PaneWheelUp, 10 * time.Millisecond, 30 * time.Millisecond},
		{'+', 3 * time.Millisecond, 33 * time.Millisecond},
		{'-', 10 * time.Millisecond, 53 * time.Millisecond},
		{' ', 7 * time.Millisecond, 60 * time.Millisecond},
		{terminalui.PaneWheelUp, time.Second, 60 * time.Millisecond},
		{' ', time.Second, 60 * time.Millisecond},
		{terminalui.PaneWheelDown, 10 * time.Millisecond, 70 * time.Millisecond},
	} {
		if quit, err := p.control(event.key, event.elapsed); err != nil || quit {
			t.Fatalf("control %q: quit=%v error=%v", event.key, quit, err)
		}
		if p.position != event.want || !p.at.Equal(p.source.Start.Add(event.want)) {
			t.Fatalf("control %q lost recorded time: position=%v want=%v", event.key, p.position, event.want)
		}
	}
}

func TestSessionUIReplayPTYWheelSpeedQuitAndRestore(t *testing.T) {
	for _, quitKey := range []struct{ name, key string }{{"q", "q"}, {"ctrl-c", "\x03"}, {"escape", "\x1b"}} {
		t.Run(quitKey.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CODEX_HOME", dir)
			t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
			sessions := filepath.Join(dir, "sessions", "2023", "11", "14")
			if err := os.MkdirAll(sessions, 0700); err != nil {
				t.Fatal(err)
			}
			var transcript strings.Builder
			for row := 1; row <= 36; row++ {
				fmt.Fprintf(&transcript, "Replay terminal row %02d.\n", row)
			}
			replayTestWrite(t, sessions, "rollout-2023-root.jsonl", replayTestMeta("root"),
				replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
				replayTestItem("root", "turn", replayTestEpoch+1, replayTestEpoch+2, map[string]any{"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": transcript.String()}}}),
				replayTestRecord("event_msg", replayTestEpoch+600000, map[string]any{"type": "task_complete", "turn_id": "turn"}))
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := pty.Setsize(master, &pty.Winsize{Cols: 120, Rows: 28}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			stderr, err := os.Create(filepath.Join(dir, "stderr"))
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			chunks := make(chan []byte, 128)
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				buffer := make([]byte, 8192)
				for {
					n, err := master.Read(buffer)
					if n > 0 {
						select {
						case chunks <- append([]byte(nil), buffer[:n]...):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			done := make(chan int, 1)
			finished := false
			go func() {
				done <- RunSessionUIReplay(ctx, []string{"--session", "root", "--from", "1s"}, slave, slave, stderr)
			}()
			defer func() {
				cancel()
				if !finished {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("replay did not stop on cancellation")
					}
				}
				slave.Close() // Wake the PTY master reader before joining it.
				master.Close()
				select {
				case <-readerDone:
				case <-time.After(3 * time.Second):
					t.Error("PTY reader did not stop")
				}
			}()
			screen := vt.NewEmulator(120, 28)
			defer screen.Close()
			var raw strings.Builder
			await := func(label string) {
				t.Helper()
				deadline := time.NewTimer(3 * time.Second)
				defer deadline.Stop()
				for {
					select {
					case chunk := <-chunks:
						raw.Write(chunk)
						if _, err := screen.Write(chunk); err != nil {
							t.Fatal(err)
						}
						if strings.Contains(screen.String(), label) {
							return
						}
					case code := <-done:
						finished = true
						data, _ := os.ReadFile(stderr.Name())
						t.Fatalf("replay exited before %q: code=%d stderr=%s", label, code, data)
					case <-deadline.C:
						t.Fatalf("missing rendered %q:\n%s", label, screen.String())
					}
				}
			}
			write := func(input string) {
				t.Helper()
				if _, err := master.Write([]byte(input)); err != nil {
					t.Fatal(err)
				}
			}
			await("1.0x")
			write(" ")
			await("Ⅱ Replay")
			if !strings.Contains(screen.String(), "Replay terminal row 36.") {
				t.Fatalf("overflowing transcript did not start at its tail:\n%s", screen.String())
			}
			// The speed key after each report also checks that report bytes
			// neither quit playback nor swallow the next ordinary control.
			write("\x1b[<64;10;5M+")
			await("2.0x")
			if strings.Contains(screen.String(), "Replay terminal row 36.") {
				t.Fatalf("wheel-up did not move the viewport:\n%s", screen.String())
			}
			// Leaving follow mode adds a scroll hint row, so a second down
			// event crosses the resized viewport's tail and resumes follow.
			write("\x1b[<65;10;5M\x1b[<65;10;5M-")
			await("1.0x")
			if !strings.Contains(screen.String(), "Replay terminal row 36.") {
				t.Fatalf("wheel-down did not restore the tail:\n%s", screen.String())
			}
			write("---")
			await("0.125x")
			if !strings.Contains(screen.String(), "Replay terminal row 36.") {
				t.Fatalf("PTY rendered controls but lost transcript:\n%s", screen.String())
			}
			write(quitKey.key)
			select {
			case code := <-done:
				finished = true
				if code != 0 {
					data, _ := os.ReadFile(stderr.Name())
					t.Fatalf("quit exit=%d stderr=%s", code, data)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("quit did not close replay")
			}
			after, err := term.GetState(int(slave.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("terminal attributes were not restored: error=%v", err)
			}
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			for !strings.Contains(raw.String(), "\x1b[?1000;1006l\x1b[0m\x1b[?25h\x1b[?1049l") {
				select {
				case chunk := <-chunks:
					raw.Write(chunk)
				case <-deadline.C:
					t.Fatal("quit did not restore mouse, cursor, and alternate-screen modes")
				}
			}
		})
	}
}
