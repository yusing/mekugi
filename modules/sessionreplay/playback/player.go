// Source: internal/router/session_ui_replay.go:89:313@[3e523196] recorded-clock scheduling.
package playback

import (
	"context"
	"errors"
	"math"
	"time"
)

type Intent uint8

const (
	Tick Intent = iota
	TogglePause
	Faster
	Slower
	SeekBackward
	SeekForward
	Restart
)

// Player belongs to one offline attachment. The caller supplies a fresh
// presentation on Reset and applies observations without executing effects.
// It has no goroutine or wall timer; the renderer supplies elapsed wall time.
type Player struct {
	Source                *Source
	At                    time.Time
	Position, From, Until time.Duration
	Speed                 float64
	Paused                bool
	Next                  int
}

func NewPlayer(source *Source, speed float64) *Player {
	return &Player{Source: source, At: source.Start, Until: source.End.Sub(source.Start), Speed: speed}
}

// Bounds validates the selected interval before any presentation is reconstructed.
func (p *Player) Bounds(from, until time.Duration) error {
	duration := p.Source.End.Sub(p.Source.Start)
	if duration > 24*time.Hour {
		return errors.New("replay exceeds 24 hours")
	}
	if math.IsNaN(p.Speed) || math.IsInf(p.Speed, 0) || p.Speed < 0.1 || p.Speed > 100 {
		return errors.New("replay speed must be finite and between 0.1 and 100")
	}
	if until == 0 {
		until = duration
	}
	if from < 0 || from >= until || until > duration {
		return errors.New("require 0 <= from < until <= session duration")
	}
	p.From, p.Until = from, until
	return nil
}

func (p *Player) Advance(ctx context.Context, position time.Duration, reset func(), apply func(Event) error) error {
	position = max(0, min(position, p.Until))
	if position < p.Position {
		p.At, p.Next = p.Source.Start, 0
		p.Position = 0
		reset()
	}
	p.Position = position
	target := p.Source.Start.Add(position)
	for p.Next < len(p.Source.Events) && !p.Source.Events[p.Next].At.After(target) {
		if err := ctx.Err(); err != nil {
			return err
		}
		event := p.Source.Events[p.Next]
		p.At = event.At
		p.Next++
		if err := apply(event); err != nil {
			return err
		}
	}
	p.At = target
	return nil
}

// Step charges elapsed time at the previous speed/pause state before applying an
// intent. Navigation-only input uses Tick and does not discard intervening time.
func (p *Player) Step(ctx context.Context, elapsed time.Duration, intent Intent, reset func(), apply func(Event) error) (sought bool, err error) {
	if !p.Paused {
		if err = p.Advance(ctx, p.Position+time.Duration(float64(elapsed)*p.Speed), reset, apply); err != nil {
			return false, err
		}
	}
	position := p.Position
	switch intent {
	case TogglePause:
		p.Paused = !p.Paused
	case Faster:
		p.Speed = speedStep(p.Speed, true)
	case Slower:
		p.Speed = speedStep(p.Speed, false)
	case SeekBackward:
		position = max(p.From, p.Position-10*time.Second)
		sought = true
	case SeekForward:
		position = min(p.Until, p.Position+10*time.Second)
		sought = true
	case Restart:
		p.Paused = false
		position = p.From
		sought = true
	}
	if sought {
		err = p.Advance(ctx, position, reset, apply)
	}
	return sought, err
}

// One ladder preserves exact presets after either speed limit is reached.
func speedStep(speed float64, faster bool) float64 {
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
