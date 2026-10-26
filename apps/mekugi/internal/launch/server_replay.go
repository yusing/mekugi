package launch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

// Source: apps/mekugi/internal/launch/web.go@14a6d1f3 webReplay presentation/control adapter.
// The server borrows the same recorded attachment as terminal playback, without
// constructing a renderer, live controller, backend, or writable history store.
type serverReplay struct {
	*playback.Player
	replayOwners
	current      *replayAttachment
	main, agents commandFeed
	ctx          context.Context
	childSummary bool
}

func (p *serverReplay) reset() {
	if p.current != nil {
		p.current.session.Retire()
	}
	p.current = p.replayOwners.attachment(p.Source)
	p.main, p.agents = commandFeed{}, commandFeed{}
}

func (p *serverReplay) apply(event playback.Event) error {
	p.current.apply(p.replayOwners, p.Source, p.At, event)
	return nil
}

func (p *serverReplay) frame() sessionapi.Frame {
	s := p.current
	refreshRosterRoles(&s.session, s.childSource)
	view := s.sessionView()
	status := view.Status.Session
	if len(p.Source.Missing)+len(p.Source.Unsupported)+len(p.Source.HookUnavailable) > 0 {
		status.Text += fmt.Sprintf("; replay coverage: %d missing children, %d unsupported item kinds, %d unavailable hook histories", len(p.Source.Missing), len(p.Source.Unsupported), len(p.Source.HookUnavailable))
		status.Alert = true
	}
	return sessionapi.Frame{
		Session:    sessionapi.Session{Thread: p.Source.Thread, Workspace: s.cwd, Started: p.Source.Start.UnixMilli()},
		Playback:   &sessionapi.Playback{Position: p.Position.Milliseconds(), Until: p.Until.Milliseconds(), Speed: p.Speed, Paused: p.Paused, Simulated: true},
		Attachment: view.Version.Attachment, Status: status,
		Main:              p.main.publication(s.source, s.source.Publication(s.presentation, nil)),
		Agents:            p.agents.publication(s.childSource, s.childSource.Publication(s.presentation, s.source)),
		ChildReplySummary: p.childSummary,
	}
}

// Elapsed time is charged before changing speed, pause state, or position.
func (p *serverReplay) control(intent sessionapi.Intent, elapsed time.Duration) error {
	if intent.Action == sessionapi.Seek {
		if _, err := p.Step(p.ctx, elapsed, playback.Tick, p.reset, p.apply); err != nil {
			return err
		}
		return p.Advance(p.ctx, max(p.From, intent.Position), p.reset, p.apply)
	}
	intents := map[sessionapi.Action]playback.Intent{
		sessionapi.TogglePause: playback.TogglePause, sessionapi.Faster: playback.Faster, sessionapi.Slower: playback.Slower,
		sessionapi.SeekBack: playback.SeekBackward, sessionapi.SeekForward: playback.SeekForward, sessionapi.Restart: playback.Restart,
	}
	_, err := p.Step(p.ctx, elapsed, intents[intent.Action], p.reset, p.apply)
	return err
}

func runServerReplay(ctx context.Context, request sessionapi.ReplayStart, api *sessionapi.Server) (err error) {
	settings, err := launchoptions.LoadSettings(false)
	if err != nil {
		return err
	}
	var featureArgs []string
	if request.ChildReplySummary != nil {
		featureArgs = []string{"--child-reply-summary=" + strconv.FormatBool(*request.ChildReplySummary)}
	}
	options, err := launchoptions.Parse(featureArgs)
	if err != nil {
		return err
	}
	summaries := options.ResolveFeatures(settings.Features).ChildReplySummary
	runtime, err := composition.Frontends(ctx, false, summaries, "", nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close(context.WithoutCancel(ctx))) }()
	seed, speed := uint64(1), 1.0
	if request.Seed != nil {
		seed = *request.Seed
	}
	if request.Speed != nil {
		speed = *request.Speed
	}
	source, err := readReplaySource(ctx, runtime.Replay, request.Session, "", seed)
	if err != nil {
		return err
	}
	p := &serverReplay{Player: playback.NewPlayer(source, speed), replayOwners: replayOwners{runtime.Presentation, runtime.Conversation, runtime.Observation, runtime.Activity}, ctx: ctx, childSummary: summaries}
	if err := p.Bounds(0, 0); err != nil {
		return err
	}
	p.reset()
	defer func() { p.current.session.Retire() }()
	if err := p.Advance(ctx, 0, p.reset, p.apply); err != nil {
		return err
	}
	frame := p.frame()
	if err := api.Publish(frame); err != nil {
		return err
	}
	if err := api.SetSummary(sessionapi.Summary{Replay: true, Thread: source.Thread, Workspace: source.Cwd, Started: source.Start, Status: frame.Status, Ready: true}); err != nil {
		return err
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case intent := <-api.Intents():
			now := time.Now()
			err = p.control(intent, now.Sub(last))
			last = now
		case <-tick.C:
			now := time.Now()
			elapsed := now.Sub(last)
			last = now
			if p.Paused {
				continue
			}
			_, err = p.Step(ctx, elapsed, playback.Tick, p.reset, p.apply)
		}
		if err != nil {
			return err
		}
		if p.Position >= p.Until {
			p.Paused = true
		}
		frame = p.frame()
		if err := api.Publish(frame); err != nil {
			return err
		}
		if err := api.SetSummary(sessionapi.Summary{Replay: true, Thread: source.Thread, Workspace: source.Cwd, Started: source.Start, Status: frame.Status, Ready: true}); err != nil {
			return err
		}
	}
}
