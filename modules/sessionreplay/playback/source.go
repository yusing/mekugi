// Source: internal/router/session_ui_replay_read.go:94:292@[3e523196] replay construction.
package playback

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/replaytiming"
	"github.com/yusing/mekugi/modules/activity/coordination"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Decoder struct {
	Read     func(context.Context, string, map[string]bool) (observation.Source, error)
	Identity func(string) (string, error)
}

type Service struct {
	history *history.Service
	timing  replaytiming.Reader
	decoder Decoder
}

func New(history *history.Service, timing []replaytiming.Reader) (*Service, error) {
	if history == nil {
		return nil, errors.New("session replay requires session history")
	}
	if len(timing) > 1 {
		return nil, errors.New("multiple replay timing contributors")
	}
	service := &Service{history: history}
	if len(timing) == 1 {
		if timing[0] == nil {
			return nil, errors.New("missing replay timing reader")
		}
		service.timing = timing[0]
	}
	return service, nil
}
func (s *Service) BindSource(decoder Decoder) error {
	if s.decoder.Read != nil {
		return errors.New("replay source already bound")
	}
	if decoder.Read == nil || decoder.Identity == nil {
		return errors.New("incomplete replay source")
	}
	s.decoder = decoder
	return nil
}

// Source owns only the bounded offline timeline, never a second live transcript.
type Source struct {
	Thread, Cwd      string
	Start, End       time.Time
	Events           []Event
	Threads          map[string]string
	Workspaces       map[string]string
	Missing          []string
	Unsupported      map[string]int
	Items, Providers int
	HookUnavailable  []string
}
type Event struct {
	observation.Event
	Hook *history.HookObservation
}

type ReadOptions struct {
	Path, HistoryDirectory, DebugDirectory string
	Seed                                   uint64
	// ThreadMetadata supplies optional read-only metadata for the decoded root.
	ThreadMetadata func(*history.Store, string) ([]observation.Thread, int, error)
}

func (s *Service) Read(ctx context.Context, options ReadOptions) (*Source, error) {
	if s.decoder.Read == nil {
		return nil, errors.New("replay source is unavailable")
	}
	r := &Source{Threads: make(map[string]string), Workspaces: make(map[string]string), Unsupported: make(map[string]int)}
	store := s.history.Attach(options.HistoryDirectory)
	rng := rand.New(rand.NewPCG(options.Seed, options.Seed^0xa0761d6478bd642f))
	queue := []string{options.Path}
	loaded := make(map[string]bool)
	nonAttachable := make(map[string]bool)
	totalBytes := 0
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := queue[0]
		queue = queue[1:]
		decoded, err := s.decoder.Read(ctx, path, loaded)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		totalBytes += decoded.Bytes
		if totalBytes > 256<<20 {
			return nil, errors.New("replay corpus exceeds 256 MiB")
		}
		thread := decoded.Thread
		if loaded[thread] {
			return nil, errors.New("duplicate thread rollout")
		}
		loaded[thread] = true
		r.Workspaces[thread] = decoded.Cwd
		if r.Thread == "" {
			r.Thread, r.Cwd = thread, decoded.Cwd
			r.Threads[thread] = "/root"
			if options.ThreadMetadata != nil {
				metadata, size, err := options.ThreadMetadata(store, thread)
				if err != nil {
					return nil, fmt.Errorf("read retained thread metadata: %w", err)
				}
				totalBytes += size
				if totalBytes > 256<<20 {
					return nil, errors.New("replay corpus exceeds 256 MiB")
				}
				for _, member := range metadata {
					if member.NonAttachable {
						nonAttachable[member.ID] = true
					}
				}
			}
		}
		for kind, n := range decoded.Unsupported {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			r.Unsupported[kind] += n
		}
		var children []string
		for _, observed := range decoded.Events {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			e := Event{Event: observed}
			if e.Kind == observation.ItemCompleted {
				r.Items++
				if e.Item.Type == "contextCompaction" {
					summary, size, err := compact.ReadSummary(store, thread, e.Item.CompactionReference)
					totalBytes += size
					if totalBytes > 256<<20 {
						return nil, errors.New("replay corpus exceeds 256 MiB")
					}
					if err != nil {
						summary = "Compaction summary unavailable: " + err.Error()
					}
					e.Item.Text = summary
				}
				if c, ok := coordination.ProjectCoordination(e.Item); ok && c.Kind == "start" && c.Confirmed && r.Threads[c.Thread] == "" {
					r.Threads[c.Thread] = c.Target
					children = append(children, c.Thread)
					if nonAttachable[c.Thread] {
						r.Events = append(r.Events, Event{
							At: e.At, Kind: "thread/started", ThreadID: c.Thread,
							Thread: observation.Thread{ID: c.Thread, Path: c.Target, NonAttachable: true}})
					}
				}
				if e.At.After(e.Started) {
					begin := e
					begin.At, begin.Kind = e.Started, observation.ItemStarted
					begin.Item.Text, begin.Item.Summary, begin.Item.AggregatedOutput = "", nil, nil
					begin.Item.Result, begin.Item.Error = nil, nil
					begin.Item.Status, begin.Item.ExitCode, begin.Item.DurationMS = "inProgress", nil, nil
					r.Events = append(r.Events, begin)
					stream, err := deltas(ctx, rng, e, e.Started, e.At)
					if err != nil {
						return nil, err
					}
					r.Events = append(r.Events, stream...)
				}
			}
			r.Events = append(r.Events, e)
			if len(r.Events) > 500000 {
				return nil, errors.New("replay exceeds 500000 events")
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hooks, err := store.ReadHookObservations(decoded.Cwd, thread)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		totalBytes += hooks.Bytes
		if totalBytes > 256<<20 {
			return nil, errors.New("replay corpus exceeds 256 MiB")
		}
		if err != nil {
			r.HookUnavailable = append(r.HookUnavailable, fmt.Sprintf("%s: %v", thread, err))
			hooks = history.HookObservations{}
		}
		if hooks.Dropped > 0 {
			r.HookUnavailable = append(r.HookUnavailable, fmt.Sprintf("%s: %d earlier observations were not retained", thread, hooks.Dropped))
		}
		for _, hook := range hooks.Runs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			r.Events = append(r.Events, Event{At: hook.At, ThreadID: thread, Hook: &hook})
			if len(r.Events) > 500000 {
				return nil, errors.New("replay exceeds 500000 events")
			}
		}
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if loaded[child] {
				continue
			}
			if strings.ContainsAny(child, `/\*?[]`) {
				return nil, errors.New("invalid child thread id")
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "rollout-*-"+child+".jsonl"))
			if err != nil {
				return nil, err
			}
			verified := matches[:0]
			for _, candidate := range matches {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				identity, err := s.decoder.Identity(candidate)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", candidate, err)
				}
				if identity == child {
					verified = append(verified, candidate)
				}
			}
			if len(verified) != 1 {
				r.Missing = append(r.Missing, child)
				continue
			}
			queue = append(queue, verified[0])
		}
		if len(loaded)+len(queue) > 128 {
			return nil, errors.New("replay exceeds 128 threads")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(r.Events) == 0 {
		return nil, errors.New("session has no timestamped UI events")
	}
	if options.DebugDirectory != "" && s.timing != nil {
		err := s.timing(ctx, filepath.Join(options.DebugDirectory, "capture.jsonl"), loaded, func(interval replaytiming.Interval) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			r.Providers++
			r.Events = append(r.Events, Event{At: interval.Start, Kind: observation.ProviderStarted, ThreadID: interval.Thread}, Event{At: interval.End, Kind: observation.ProviderCompleted, ThreadID: interval.Thread})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(r.Events, func(a, b Event) int { return a.At.Compare(b.At) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.Start, r.End = r.Events[0].At, r.Events[len(r.Events)-1].At
	return r, nil
}

// Source: internal/router/session_ui_replay_read.go:399:442@[3e523196] replayDeltas.
func deltas(ctx context.Context, rng *rand.Rand, endEvent Event, start, end time.Time) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i := endEvent.Item
	text, kind, chunk := i.Text, observation.AgentDelta, 12
	switch i.Type {
	case "agentMessage":
		if len(i.Questions) > 0 {
			return nil, nil
		}
	case "reasoning":
		text, kind = strings.Join(i.Summary, "\n\n"), observation.ReasoningDelta
	case "commandExecution":
		if i.AggregatedOutput == nil {
			return nil, nil
		}
		text, kind, chunk = *i.AggregatedOutput, observation.CommandDelta, 96
	default:
		return nil, nil
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return nil, nil
	}
	n := min(2048, max(1, len(runes)/chunk), max(1, int(end.Sub(start)/(35*time.Millisecond))))
	weights := make([]int, n)
	total := 0
	for j := range weights {
		weights[j] = 1 + rng.IntN(7)
		total += weights[j]
	}
	var events []Event
	at, pos := 0, 0
	for j, w := range weights {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		at += w
		next := len(runes) * (j + 1) / n
		e := Event{At: start.Add(time.Duration(float64(end.Sub(start)) * 0.95 * float64(at) / float64(total))), Kind: kind, ThreadID: endEvent.ThreadID, TurnID: endEvent.TurnID, ItemID: i.ID, Delta: string(runes[pos:next])}
		// Agent message deltas share the same typed identity as live messages.
		if i.Type == "agentMessage" {
			e.Item = observation.Item{Type: i.Type, ID: i.ID}
		}
		events = append(events, e)
		pos = next
	}
	return events, nil
}
