package presentation

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/scope"
)

const SupplementMessageBytes = 64 << 10

// SupplementEditReceipt annotates a retained host call, never a new tool event.
// Calls identifies the original host items whose captured effects Text describes.
const SupplementEditReceipt = "edit_receipt"

// ReloadSource is an orchestration-issued attachment intent. Host events own its lifecycle.
type ReloadSource struct {
	Command, Path, Skill string
	ExitCode             *int // Present only after an authenticated per-source result.
}
type Supplement struct {
	ReloadSources                      []ReloadSource
	Thread, Source, Kind, Text, CallID string
	Turn                               string
	Calls                              []string // Ordered host identities for an edit receipt.
	Observed                           time.Time
}
type RequestStart struct {
	Thread string
	At     time.Time
}
type Unreturned struct{ Thread, Call string }

// Audience is a detached application-authenticated routing snapshot, not a roster.
type Audience struct {
	Root    string
	Threads map[string]string
}

type queuedSupplement struct {
	Supplement
	queued time.Time
}
type supplementThread struct {
	seen        map[string]struct{}
	repetitions map[string]string
}

// Supplements owns invocation-scoped auxiliary observations. Its application
// serializes access together with the supplied thread scopes and authority facts.
// Source: apps/mekugi/internal/router/subagent_activity{,_pane}.go@d002a0dc.
type Supplements struct {
	threads    scope.Key[supplementThread]
	root       string
	borrowed   string
	events     []queuedSupplement
	starts     []RequestStart
	unreturned []Unreturned
}

var invocationSupplements scope.Key[Supplements]

func RequestSupplements(invocation *scope.Store) *Supplements {
	return invocationSupplements.Value(invocation)
}

func (s *Supplements) Attached(root string) bool {
	return root != "" && (s.root == root || s.borrowed == root)
}

// Borrow selects the one independently owned auxiliary audience. Empty releases it.
func (s *Supplements) Borrow(root string) { s.borrowed = root }

func (s *Supplements) Attach(root string) { s.root = root }
func (s *Supplements) Detach()            { s.root = "" }
func (s *Supplements) Clear() {
	s.root, s.borrowed = "", ""
	s.events, s.starts, s.unreturned = nil, nil, nil
}

func supplementID(source string) string {
	digest := sha256.Sum256([]byte(source))
	return fmt.Sprintf("msg_mekugi_commentary_%x", digest[:12])
}

// Collect returns true only when capacity omitted an otherwise eligible update.
// Callers admit native identity before supplying its scope. Source recognition
// never grants execution or filesystem authority.
func (s *Supplements) Collect(thread *scope.Store, event Supplement, now time.Time) bool {
	if event.Source == "" || len(event.Source) > 16<<10 || strings.TrimSpace(event.Text) == "" {
		return false
	}
	switch event.Kind {
	case "compact_reload":
		event.ReloadSources = slices.Clone(event.ReloadSources)
	case SupplementEditReceipt:
		if len(event.Calls) == 0 || slices.Contains(event.Calls, "") {
			return false
		}
		event.Calls = slices.Clone(event.Calls)
	case "output_dedupe", "error":
	default:
		return false
	}
	state := s.threads.Value(thread)
	if state.seen == nil {
		state.seen = make(map[string]struct{})
	}
	event.Source = supplementID(event.Source)
	if _, exists := state.seen[event.Source]; exists {
		return false
	}
	if event.Kind != "error" {
		event.Text = ClipSupplementMessage(event.Text)
	}
	s.expire(now)
	count := 0
	for _, queued := range s.events {
		if queued.Thread == event.Thread {
			count++
		}
	}
	if len(s.events) >= 1024 || count >= 64 {
		return true
	}
	state.seen[event.Source] = struct{}{}
	if event.Kind == "output_dedupe" && event.CallID != "" {
		if state.repetitions == nil {
			state.repetitions = make(map[string]string)
		}
		state.repetitions[event.CallID] = event.Text
	}
	if event.Observed.IsZero() {
		event.Observed = now
	}
	s.events = append(s.events, queuedSupplement{Supplement: event, queued: now})
	return false
}

// ClipSupplementMessage bounds non-error auxiliary display text.
func ClipSupplementMessage(text string) string {
	if len(text) <= SupplementMessageBytes {
		return text
	}
	const clipped = "\n… (message clipped at the native Activity 64 KiB display limit)"
	return strings.ToValidUTF8(text[:SupplementMessageBytes-len(clipped)], "") + clipped
}

func (s *Supplements) expire(now time.Time) {
	s.events = slices.DeleteFunc(s.events, func(event queuedSupplement) bool { return now.Sub(event.queued) >= time.Hour })
}

func (s *Supplements) Take(audience Audience, now time.Time) []Supplement {
	if !s.Attached(audience.Root) {
		return nil
	}
	s.expire(now)
	var result []Supplement
	kept := s.events[:0]
	for _, event := range s.events {
		if _, admitted := audience.Threads[event.Thread]; !admitted {
			kept = append(kept, event)
			continue
		}
		result = append(result, event.Supplement)
	}
	clear(s.events[len(kept):])
	s.events = kept
	return result
}

func (s *Supplements) StartRequest(thread string, at time.Time) {
	if (s.root != "" || s.borrowed != "") && len(s.starts) < 64 {
		s.starts = append(s.starts, RequestStart{Thread: thread, At: at})
	}
}
func (s *Supplements) TakeStarts(audience Audience) []RequestStart {
	if !s.Attached(audience.Root) {
		return nil
	}
	var starts []RequestStart
	kept := s.starts[:0]
	for _, start := range s.starts {
		if _, admitted := audience.Threads[start.Thread]; admitted {
			starts = append(starts, start)
		} else {
			kept = append(kept, start)
		}
	}
	clear(s.starts[len(kept):])
	s.starts = kept
	return starts
}
func (s *Supplements) NoteUnreturned(thread string, calls []string) {
	if s.root == "" && s.borrowed == "" {
		return
	}
	for _, call := range calls {
		if len(s.unreturned) < 256 {
			s.unreturned = append(s.unreturned, Unreturned{Thread: thread, Call: call})
		}
	}
}
func (s *Supplements) TakeUnreturned(audience Audience) []Unreturned {
	if !s.Attached(audience.Root) {
		return nil
	}
	var refs []Unreturned
	kept := s.unreturned[:0]
	for _, ref := range s.unreturned {
		if _, admitted := audience.Threads[ref.Thread]; admitted {
			refs = append(refs, ref)
		} else {
			kept = append(kept, ref)
		}
	}
	clear(s.unreturned[len(kept):])
	s.unreturned = kept
	return refs
}

// Repetition retains the delivered annotation independently of its transient
// event and attached feed. Callers must authenticate the requested thread.
func (s *Supplements) Repetition(root string, thread *scope.Store, id string) string {
	if !s.Attached(root) {
		return ""
	}
	return s.threads.Value(thread).repetitions[id]
}
