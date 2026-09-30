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
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"golang.org/x/term"
)

type uiReplayPlayback struct {
	paintedAt             time.Time
	source                *sessionUIReplay
	ui                    *appServerUI
	at                    time.Time
	position, from, until time.Duration
	speed                 float64
	paused                bool
	next                  int
	frames                []time.Duration
	writeTime             time.Duration
	ctx                   context.Context
}

type replayDiscard struct{ io.Writer }

func (replayDiscard) Close() error { return nil }

func newUIReplayPlayback(ctx context.Context, source *sessionUIReplay, speed float64) *uiReplayPlayback {
	p := &uiReplayPlayback{source: source, ctx: ctx, speed: speed, until: source.End.Sub(source.Start)}
	p.reset()
	return p
}

func (p *uiReplayPlayback) reset() {
	if p.ui != nil {
		p.close()
	}
	p.at, p.next = p.source.Start, 0
	p.position, p.paintedAt = 0, time.Time{}
	// The real message/projector/render path, but no app-server process, router,
	// replay store, filesystem observers, input submission, or notification sink.
	u := &appServerUI{ctx: p.ctx, client: &appserver.Client{Input: replayDiscard{io.Discard}}, view: newLiveActivityView(), agents: newLiveActivityView(), thread: p.source.Thread, status: "Replay", requests: make(map[string]string), clock: func() time.Time { return p.at }, replay: p}
	u.view.clock, u.agents.clock = u.clock, u.clock
	u.session.start(p.source.Thread, p.source.Cwd)
	for id := range p.source.Threads {
		if id != p.source.Thread {
			u.session.metadata[id] = ""
		}
	}
	u.ensureShell()
	u.shell.journalOpen = false
	p.ui = u
}

func (p *uiReplayPlayback) close() {
	p.ui.shell.diff.close()
	p.ui.shell.diffScreen.Close()
}

func (p *uiReplayPlayback) advance(position time.Duration) error {
	position = max(0, min(position, p.until))
	if position < p.position {
		p.reset()
	}
	p.position = position
	target := p.source.Start.Add(position)
	for p.next < len(p.source.Events) && !p.source.Events[p.next].At.After(target) {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		e := p.source.Events[p.next]
		if id := e.Params.ThreadID; id != "" && id != p.source.Thread && p.ui.session.paths[id] == "" {
			p.ui.session.registerThread(appServerThreadInfo{ID: id, AgentNickname: strings.TrimPrefix(p.source.Threads[id], "/root/")})
		}
		p.at = e.At
		p.next++
		switch e.Method {
		case "replay/providerStarted":
			p.ui.applyActivity(p.ui.session.beginThinking(e.Params.ThreadID, e.At), nil)
		case "replay/providerCompleted":
			// Retain this boundary in ordering and duration without duplicating state.
		default:
			params, err := json.Marshal(&e.Params)
			if err != nil {
				return err
			}
			if err = p.ui.message(appserver.Message{Method: e.Method, Params: params}); err != nil {
				return err
			}
		}
	}
	p.at = target
	if position == p.until {
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
	for _, run := range p.ui.session.summaries {
		if run.done != nil {
			run.output.Flush()
			run.dirty = true
		}
	}
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
	if !p.at.Equal(p.paintedAt) {
		u.flushStreamOutput()
		settleActivity(p.at, u.view, u.agents)
		u.view.pace(p.at)
		u.agents.pace(p.at)
		p.paintedAt = p.at
	}
	u.view.expireFlash(p.at)
	u.agents.expireFlash(p.at)
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
	if p.paused {
		state = "Ⅱ"
	}
	if p.position >= p.until {
		state = "■"
	}
	speed := strconv.FormatFloat(p.speed, 'f', -1, 64)
	if !strings.Contains(speed, ".") {
		speed += ".0"
	}
	label := fmt.Sprintf(" %s Replay · simulated · %sx · %s/%s", state, speed, replayTime(p.position), replayTime(p.until))
	hint := "  Space pause · +/- speed · [/] seek · r restart · q quit"
	remaining := width - ansi.StringWidth(label) - ansi.StringWidth(hint) - 3
	if remaining >= 6 {
		filled := 0
		if p.until > 0 {
			filled = int(float64(remaining) * float64(p.position) / float64(p.until))
		}
		label += " " + strings.Repeat("━", min(remaining, filled)) + strings.Repeat("─", max(0, remaining-filled))
	}
	if width >= 100 {
		label += hint
	}
	return p.ui.view.painter.Theme.Accent() + ansi.Truncate(label, max(1, width), "…") + activityui.Reset
}

func replayTime(d time.Duration) string {
	seconds := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}

// key is deliberately not routed to the live composer. Playback controls can
// never execute recorded commands, answer questions, or launch external tools.
func (p *uiReplayPlayback) key(key byte) (bool, error) {
	switch key {
	case 'q', 3, 27:
		return true, nil
	case ' ':
		p.paused = !p.paused
	case '+', '=':
		p.speed = replaySpeedStep(p.speed, true)
	case '-':
		p.speed = replaySpeedStep(p.speed, false)
	case '[':
		return false, p.seek(max(p.from, p.position-10*time.Second))
	case ']':
		return false, p.seek(min(p.until, p.position+10*time.Second))
	case 'r':
		p.paused = false
		return false, p.seek(p.from)
	case 'k':
		p.ui.view.scrollKey('b')
	case 'j':
		p.ui.view.scrollKey(' ')
	case terminalui.PaneWheelUp, terminalui.PaneWheelDown:
		p.ui.view.scrollKey(key)
	}
	return false, nil
}

// Charge elapsed wall time at the previous speed and pause state before a
// control changes them. Scrolling must not discard time between paint ticks.
func (p *uiReplayPlayback) control(key byte, elapsed time.Duration) (bool, error) {
	if !p.paused {
		if err := p.advance(p.position + time.Duration(float64(elapsed)*p.speed)); err != nil {
			return false, err
		}
	}
	return p.key(key)
}

// A shared ladder avoids drifting off 1x after either speed limit is reached.
// Arbitrary --speed values move to the next preset in the requested direction.
func replaySpeedStep(speed float64, faster bool) float64 {
	steps := [...]float64{0.1, 0.125, 0.25, 0.5, 1, 2, 4, 8, 16, 32, 64, 100}
	if faster {
		for _, step := range steps {
			if step > speed {
				return step
			}
		}
		return steps[len(steps)-1]
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i] < speed {
			return steps[i]
		}
	}
	return steps[0]
}

// RunSessionUIReplay is an offline entry point. It shares the native presenter,
// but constructs no host, network connection, execution capability, or store.
func RunSessionUIReplay(ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
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
	cpu := f.String("cpu-profile", "", "write CPU pprof to a new file")
	heap := f.String("heap-profile", "", "write heap pprof to a new file")
	f.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mekugi replay-session --session ID [options]\nOffline native UI playback. Recorded item timing; simulated streaming, not a screen recording.")
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
	source, err := readSessionUIReplay(ctx, paths[0], *debug, *seed)
	if err != nil {
		return fail(err)
	}
	duration := source.End.Sub(source.Start)
	if duration > 24*time.Hour {
		return fail(errors.New("replay exceeds 24 hours"))
	}
	if *until == 0 {
		*until = duration
	}
	if *from >= *until || *until > duration {
		return fail(errors.New("require 0 <= from < until <= session duration"))
	}
	fmt.Fprintf(stderr, "Replay: %s · %d items · %d events · %d provider calls · %s · %.1fx\nStreaming is simulated (seed %d); no recorded input, resize, journal or live-diff stream.\n", source.Thread, source.Items, len(source.Events), source.Providers, duration.Round(time.Millisecond), *speed, *seed)
	if len(source.Missing) > 0 || len(source.Unsupported) > 0 {
		fmt.Fprintf(stderr, "Coverage: %d missing child rollouts; unsupported item kinds: %v\n", len(source.Missing), source.Unsupported)
	}

	p := newUIReplayPlayback(ctx, source, *speed)
	defer p.close()
	p.from, p.until = *from, *until
	if err = p.seek(*from); err != nil {
		return fail(err)
	}
	var cpuFile, heapFile *os.File
	for _, entry := range []struct {
		path string
		dest **os.File
	}{{*cpu, &cpuFile}, {*heap, &heapFile}} {
		if entry.path == "" {
			continue
		}
		file, err := os.OpenFile(entry.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fail(err)
		}
		*entry.dest = file
		defer file.Close()
	}
	if cpuFile != nil {
		if err = pprof.StartCPUProfile(cpuFile); err != nil {
			return fail(err)
		}
	}
	started := time.Now()
	run := func(keys <-chan byte, out io.Writer) error {
		var input uiReplayInput
		defer input.close()
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		last, progress := time.Now(), time.Now()
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
		if err := paint(); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-input.escapeC:
				// Only a standalone Escape quits; CSI/SS3/mouse reports do not.
				return nil
			case key, ok := <-keys:
				if !ok {
					return io.EOF
				}
				key = input.consume(key)
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
			case <-tick.C:
				now := time.Now()
				if !p.paused {
					if err := p.advance(p.position + time.Duration(float64(now.Sub(last))*p.speed)); err != nil {
						return err
					}
				}
				last = now
				if p.paused {
					continue
				}
				if err := paint(); err != nil {
					return err
				}
				if *headless && now.Sub(progress) >= 5*time.Second {
					fmt.Fprintf(stderr, "Replay %s/%s · %d/%d events · %d frames\n", replayTime(p.position), replayTime(p.until), p.next, len(source.Events), len(p.frames))
					progress = now
				}
				if p.position >= p.until {
					if *headless {
						return nil
					}
					p.paused = true
				}
			}
		}
	}
	if *headless {
		err = run(nil, io.Discard)
	} else {
		err = terminalui.WithRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1000;1006h", "\x1b[?1000;1006l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error { return run(keys, stdout) })
	}
	elapsed := time.Since(started)
	if cpuFile != nil {
		pprof.StopCPUProfile()
	}
	if heapFile != nil {
		err = errors.Join(err, pprof.WriteHeapProfile(heapFile))
	}
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
	}{source.Thread, p.speed, *seed, true, p.next, len(p.frames), elapsed.Seconds(), p.position.Seconds(), quantile(.5), quantile(.95), quantile(.99), quantile(1), float64(p.writeTime) / float64(time.Millisecond)}
	if err = json.MarshalWrite(stdout, &summary); err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout)
	return 0
}
