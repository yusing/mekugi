package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/contracts/scope"

	activitybinding "github.com/yusing/mekugi/modules/activity/codexbinding"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

type uiReplayPlayback struct {
	*playback.Player
	activity  *activitymodel.Service
	paintedAt time.Time
	ui        *appServerUI
	prefix    bool
	frames    []time.Duration
	writeTime time.Duration
	ctx       context.Context
}

type replayDiscard struct{ io.Writer }

func (replayDiscard) Close() error { return nil }

func newUIReplayPlayback(activity *activitymodel.Service, ctx context.Context, source *playback.Source, speed float64) *uiReplayPlayback {
	p := &uiReplayPlayback{activity: activity, ctx: ctx, Player: playback.NewPlayer(source, speed)}
	p.reset()
	return p
}

func (p *uiReplayPlayback) reset() {
	var previous *terminalUI
	if p.ui != nil {
		previous = p.ui.shell
		p.close()
	}
	p.paintedAt = time.Time{}
	// The real message/projector/render path, but no app-server process, router,
	// replay store, filesystem observers, input submission, or notification sink.
	client := &appserver.Client{Input: replayDiscard{io.Discard}}
	observations := activitybinding.Codex.Observation(p.activity)
	u := &appServerUI{session: appServerSession{activity: p.activity, observations: &observations}, ctx: p.ctx, client: client, view: newActivityAdapter(), agents: newActivityAdapter(), Thread: p.Source.Thread, status: "Replay", requests: sessioncontrol.NewRequests[appServerRequest](client), clock: func() time.Time { return p.At }, replay: p}
	u.view.Clock, u.agents.Clock = u.clock, u.clock
	u.session.start(p.Source.Thread, p.Source.Cwd)
	for id := range p.Source.Threads {
		if id != p.Source.Thread {
			u.session.metadata[id] = ""
		}
	}
	u.ensureShell()
	if previous != nil {
		u.shell.Focus = previous.Focus
		u.shell.DiffOpen = previous.DiffOpen
	}
	p.ui = u
}

func (p *uiReplayPlayback) close() {
	p.ui.commits.Close()
	p.ui.shell.diff.close()
	p.ui.shell.diffScreen.Close()
	p.ui.session.state = scope.Store{}
	p.ui.session.threadStates = nil
	p.ui.session.turnStates = nil
}

func (p *uiReplayPlayback) apply(e playback.Event) error {
	if id := e.ThreadID; id != "" && id != p.Source.Thread && p.ui.session.paths[id] == "" {
		p.ui.session.registerThread(appServerThreadInfo{ID: id, AgentNickname: strings.TrimPrefix(p.Source.Threads[id], "/root/")})
	}
	switch {
	case e.Hook != nil:
		entry := p.ui.session.hookEntry(e.ThreadID, *e.Hook, false)
		entry.Seq = p.ui.session.next()
		p.ui.applyActivity([]activityPaneEntry{entry}, nil)
	case e.Kind == observation.ProviderStarted:
		p.ui.applyActivity(p.ui.transcriptUpdates(p.ui.session.providerStarted(e.ThreadID, e.At), false), nil)
	case e.Kind == observation.ProviderCompleted:
	default:
		if p.ui.replayTranscript(e.Event) {
			return nil
		}
		return p.ui.observedEvent(string(e.Kind), replayPresentationEvent(e.Event))
	}
	return nil
}
func (p *uiReplayPlayback) advance(position time.Duration) error {
	if err := p.Player.Advance(p.ctx, position, p.reset, p.apply); err != nil {
		return err
	}
	if p.Position == p.Until {
		p.settleCompleted()
	}
	p.ui.dirty = true
	return nil
}
func (p *uiReplayPlayback) step(elapsed time.Duration, intent playback.Intent) error {
	sought, err := p.Player.Step(p.ctx, elapsed, intent, p.reset, p.apply)
	if err != nil {
		return err
	}
	if sought || p.Position == p.Until {
		p.settleCompleted()
	}
	p.ui.dirty = true
	return nil
}

// Seeking skips paint ticks, so completed output must not survive as running
// merely because its synthetic chunks have not rolled through the viewport.
func (p *uiReplayPlayback) seek(position time.Duration) error {
	if err := p.advance(position); err != nil {
		return err
	}
	p.settleCompleted()
	return nil
}

func (p *uiReplayPlayback) settleCompleted() {
	p.ui.summaryStream.FinishCompleted()
	for _, run := range p.ui.session.commands {
		if run.done != nil {
			run.output.Flush()
			run.dirty = true // Flush consumed the work that roll would otherwise detect.
		}
	}
	p.ui.flushStreamOutput()
}

func (p *uiReplayPlayback) paint(out io.Writer, width, height int) error {
	start := time.Now()
	u := p.ui
	if !p.At.Equal(p.paintedAt) {
		u.flushStreamOutput()
		settleActivity(p.At, u.view, u.agents)
		u.view.Pace(p.At)
		u.agents.Pace(p.At)
		p.paintedAt = p.At
	}
	u.view.ExpireFlash(p.At)
	u.agents.ExpireFlash(p.At)
	w := &replayTimedWriter{Writer: out}
	err := u.paint(w, width, height)
	p.writeTime += w.elapsed
	p.frames = append(p.frames, time.Since(start))
	return err
}

type replayTimedWriter struct {
	io.Writer
	elapsed time.Duration
}

func (w *replayTimedWriter) Write(b []byte) (int, error) {
	start := time.Now()
	n, err := w.Writer.Write(b)
	w.elapsed += time.Since(start)
	return n, err
}

func (p *uiReplayPlayback) bar(width int) string {
	state := "▶"
	if p.Paused {
		state = "Ⅱ"
	}
	if p.Position >= p.Until {
		state = "■"
	}
	speed := strconv.FormatFloat(p.Speed, 'f', -1, 64)
	if !strings.Contains(speed, ".") {
		speed += ".0"
	}
	label := fmt.Sprintf(" %s Replay · simulated · %sx · %s/%s", state, speed, replayTime(p.Position), replayTime(p.Until))
	hint := "  ^B 1-3 · p pause · +/- speed · [/] seek · r restart · q quit"
	remaining := width - ansi.StringWidth(label) - ansi.StringWidth(hint) - 3
	if remaining >= 6 {
		filled := 0
		if p.Until > 0 {
			filled = int(float64(remaining) * float64(p.Position) / float64(p.Until))
		}
		label += " " + strings.Repeat("━", min(remaining, filled)) + strings.Repeat("─", max(0, remaining-filled))
	}
	if width >= 100 {
		label += hint
	}
	return p.ui.view.Painter.Theme.Accent() + ansi.Truncate(label, max(1, width), "…") + terminalui.Reset
}

func replayTime(d time.Duration) string {
	seconds := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}

// key is deliberately not routed to the live composer. Playback controls can
// never execute recorded commands, answer questions, or launch external tools.
func (p *uiReplayPlayback) key(key byte) (bool, error) {
	shell := p.ui.shell
	if key != 3 && shell.output != nil && (shell.output.Typing || !strings.ContainsRune("p+-=[]r", rune(key))) {
		navigation := terminalsession.ReplayNavigation(key)
		if navigation == "" {
			navigation = string(key)
		}
		if key == terminalui.PaneWheelUp {
			navigation = "k"
		} else if key == terminalui.PaneWheelDown {
			navigation = "j"
		}
		shell.outputKey(navigation)
		return false, nil
	}
	if key == 'q' || key == 3 || key == 27 {
		return true, nil
	}
	if p.prefix {
		p.prefix = false
		if key == '1' || key == '2' {
			p.ui.shell.selectNativePane(int(key - '1'))
		}
		if key == '3' {
			p.ui.shell.selectNativePane(3)
		}
		return false, nil
	}
	if key == 5 {
		return false, shell.send("\x05")
	}
	switch key {
	case 2:
		p.prefix = true
	case ' ', 'p':
		return false, p.step(0, playback.TogglePause)
	case '+', '=':
		return false, p.step(0, playback.Faster)
	case '-':
		return false, p.step(0, playback.Slower)
	case '[':
		return false, p.step(0, playback.SeekBackward)
	case ']':
		return false, p.step(0, playback.SeekForward)
	case 'r':
		return false, p.step(0, playback.Restart)
	case 'k':
		p.ui.transcriptView().ScrollKey('b')
	case 'j':
		p.ui.transcriptView().ScrollKey(' ')
	case terminalui.PaneWheelUp, terminalui.PaneWheelDown:
		p.ui.transcriptView().ScrollKey(key)
	}
	return false, nil
}

// Charge elapsed wall time at the previous speed and pause state before a
// control changes them. Scrolling must not discard time between paint ticks.
func (p *uiReplayPlayback) control(key byte, elapsed time.Duration) (bool, error) {
	if err := p.step(elapsed, playback.Tick); err != nil {
		return false, err
	}
	return p.key(key)
}

// run owns replay input, clock and rendering lifecycles. Paint supplies the
// current terminal geometry without participating in input admission.
func (p *uiReplayPlayback) run(keys <-chan byte, paint func() error, headless bool, stderr io.Writer) error {
	var input terminalsession.ReplayInput
	defer input.Close()
	tick := time.NewTicker(33 * time.Millisecond)
	defer tick.Stop()
	last, progress := time.Now(), time.Now()
	if err := paint(); err != nil {
		return err
	}
	for {
		var key byte
		var open bool
		select {
		case <-p.ctx.Done():
			return p.ctx.Err()
		case <-input.EscapeReady():
			// A slow paint may leave both the deadline and continuation ready.
			// Give one queued byte precedence, then return to lifecycle dispatch.
			select {
			case key, open = <-keys:
			default:
				input.ExpireEscape()
				if p.ui.shell.output != nil {
					p.ui.shell.outputKey("\x1b")
					if err := paint(); err != nil {
						return err
					}
					continue
				}
				return nil
			}
		case key, open = <-keys:
		case <-tick.C:
			now := time.Now()
			if !p.Paused {
				if err := p.step(now.Sub(last), playback.Tick); err != nil {
					return err
				}
			}
			last = now
			if p.Paused {
				continue
			}
			if err := paint(); err != nil {
				return err
			}
			if headless && now.Sub(progress) >= 5*time.Second {
				fmt.Fprintf(stderr, "Replay %s/%s · %d/%d events · %d frames\n", replayTime(p.Position), replayTime(p.Until), p.Next, len(p.Source.Events), len(p.frames))
				progress = now
			}
			if p.Position >= p.Until {
				if headless {
					return nil
				}
				p.Paused = true
			}
			continue
		}
		if !open {
			return io.EOF
		}
		key = input.Consume(key)
		if key == 0 {
			continue
		}
		now := time.Now()
		quit, err := p.control(key, now.Sub(last))
		last = now
		if err != nil || quit {
			return err
		}
		if err = paint(); err != nil {
			return err
		}
	}
}

// RunSessionUIReplay is an offline entry point. It shares the native presenter,
// but constructs no host, upstream connection, execution capability, or store.
func RunSessionUIReplay(activity *activitymodel.Service, service *playback.Service, ctx context.Context, args []string, stdin, stdout, stderr *os.File) (code int) {
	f := flag.NewFlagSet("replay-session", flag.ContinueOnError)
	f.SetOutput(stderr)
	session := f.String("session", "", "Codex session ID (required; searches CODEX_HOME or ~/.codex)")
	debug := f.String("debug-dir", "", "optional debug bundle with provider timing capture.jsonl")
	speed := f.Float64("speed", 1, "playback speed, 0.1 to 100 (1 = original wall timing)")
	seed := f.Uint64("seed", 1, "repeatable seed for simulated streaming cadence")
	headless := f.Bool("headless", false, "render offline without a terminal; write timing summary JSON")
	width := f.Int("width", 160, "headless terminal columns")
	height := f.Int("height", 48, "headless terminal rows")
	from := f.Duration("from", 0, "start at this recorded offset, reconstructing earlier state")
	until := f.Duration("until", 0, "stop at this recorded offset (default session end)")
	f.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mekugi replay-session --session ID [options]\nOffline UI playback. Recorded item timing; simulated streaming, not a screen recording.")
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "mekugi replay-session:", err); return 1 }
	if f.NArg() != 0 || strings.TrimSpace(*session) == "" || strings.ContainsAny(*session, `/\*?[]`) || math.IsNaN(*speed) || math.IsInf(*speed, 0) || *speed < 0.1 || *speed > 100 || *width < 20 || *width > 1000 || *height < 8 || *height > 500 || *from < 0 || *until < 0 {
		f.Usage()
		return 2
	}
	if !*headless && (!term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd()))) {
		return fail(errors.New("interactive playback requires a terminal; use --headless for profiling"))
	}
	fmt.Fprintln(stderr, "Loading recorded session and child items…")
	rollouts, err := discoverDebugRollouts(ctx, []string{*session})
	if err != nil {
		return fail(err)
	}
	paths := rollouts[*session]
	if len(paths) == 0 {
		return fail(fmt.Errorf("session %q not found in Codex sessions or archived_sessions", *session))
	}
	if len(paths) != 1 {
		return fail(fmt.Errorf("session %q has multiple rollouts in Codex sessions or archived_sessions", *session))
	}
	directory, _ := ReplayDirectory()
	source, err := service.Read(ctx, playback.ReadOptions{Path: paths[0], HistoryDirectory: directory, DebugDirectory: *debug, Seed: *seed})
	if err != nil {
		return fail(err)
	}
	duration := source.End.Sub(source.Start)
	p := newUIReplayPlayback(activity, ctx, source, *speed)
	defer p.close()
	if err = p.Bounds(*from, *until); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stderr, "Replay: %s · %d items · %d events · %d provider calls · %s · %.1fx\nStreaming is simulated (seed %d); no recorded input, resize or live-diff stream.\n", source.Thread, source.Items, len(source.Events), source.Providers, activityui.FormatDuration(duration), *speed, *seed)
	if len(source.Missing) > 0 || len(source.Unsupported) > 0 {
		fmt.Fprintf(stderr, "Coverage: %d missing child rollouts; unsupported item kinds: %v\n", len(source.Missing), source.Unsupported)
	}
	for _, failure := range source.HookUnavailable {
		fmt.Fprintf(stderr, "Coverage: hook history unavailable (%s).\n", failure)
	}
	if err = p.seek(*from); err != nil {
		return fail(err)
	}
	stopProfiling, err := startProfiling(ctx, stderr)
	if err != nil {
		return fail(err)
	}
	defer func() {
		if err := stopProfiling(); err != nil {
			code = fail(err)
		}
	}()
	started := time.Now()
	run := func(keys <-chan byte, out io.Writer) error {
		paint := func() error {
			w, h := *width, *height
			if !*headless {
				var err error
				w, h, err = term.GetSize(int(stdout.Fd()))
				if err != nil {
					return err
				}
			}
			return p.paint(out, w, h)
		}
		return p.run(keys, paint, *headless, stderr)
	}
	if *headless {
		err = run(nil, io.Discard)
	} else {
		err = terminalui.WithRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1000;1006h", "\x1b[?1000;1006l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error { return run(keys, stdout) })
	}
	elapsed := time.Since(started)
	if err != nil {
		return fail(err)
	}
	slices.Sort(p.frames)
	quantile := func(q float64) float64 {
		if len(p.frames) == 0 {
			return 0
		}
		return float64(p.frames[int(float64(len(p.frames)-1)*q)]) / float64(time.Millisecond)
	}
	summary := struct {
		Session   string  `json:"session"`
		Speed     float64 `json:"speed"`
		Seed      uint64  `json:"seed"`
		Simulated bool    `json:"simulated_streaming"`
		Events    int     `json:"events_applied"`
		Frames    int     `json:"frames"`
		Elapsed   float64 `json:"wall_seconds"`
		Position  float64 `json:"recorded_seconds"`
		P50       float64 `json:"frame_p50_ms"`
		P95       float64 `json:"frame_p95_ms"`
		P99       float64 `json:"frame_p99_ms"`
		Max       float64 `json:"frame_max_ms"`
		Writes    float64 `json:"write_ms"`
	}{source.Thread, p.Speed, *seed, true, p.Next, len(p.frames), elapsed.Seconds(), p.Position.Seconds(), quantile(.5), quantile(.95), quantile(.99), quantile(1), float64(p.writeTime) / float64(time.Millisecond)}
	if err = json.MarshalWrite(stdout, &summary); err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout)
	return 0
}
