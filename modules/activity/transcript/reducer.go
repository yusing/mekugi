// Package transcript reduces admitted text facts into the existing Activity feed.
package transcript

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
)

type pending struct {
	item string
	at   time.Time
}

// State owns only transient correlation. Feed records and outputs remain with
// their existing owners and attachment lifetime.
// Source: apps/mekugi/internal/router/app_server_events.go:567:637@[453772ac].
type State struct {
	reasoning map[observation.ItemRef]string // Active bodies; completed identities retain empty tombstones.
	thinking  map[observation.ItemRef]time.Time
	pending   map[string]pending
	messages  map[string]observation.TextUpdate
	finals    map[string]bool
	sequence  uint64
}

func (s *State) init() {
	if s.reasoning != nil {
		return
	}
	s.reasoning = make(map[observation.ItemRef]string)
	s.thinking = make(map[observation.ItemRef]time.Time)
	s.pending = make(map[string]pending)
	s.messages = make(map[string]observation.TextUpdate)
	s.finals = make(map[string]bool)
}

func (s *State) TurnStarted(thread string) {
	s.init()
	s.finals[thread] = false
	delete(s.messages, thread)
}

func (s *State) ProviderStarted(thread string, at time.Time) []observation.TextUpdate {
	s.init()
	for key := range s.thinking {
		if key.ThreadID == thread {
			return nil
		}
	}
	updates := s.drop(thread)
	s.sequence++
	item := "thinking:" + strconv.FormatUint(s.sequence, 10)
	s.pending[thread] = pending{item, at}
	return append(updates, observation.TextUpdate{Ref: observation.ItemRef{ThreadID: thread, ItemID: item}, Kind: "reasoning", Phase: "summary", Observed: at, Live: true})
}

func (s *State) drop(thread string) []observation.TextUpdate {
	p, ok := s.pending[thread]
	if !ok {
		return nil
	}
	delete(s.pending, thread)
	return []observation.TextUpdate{{Ref: observation.ItemRef{ThreadID: thread, ItemID: p.item}, Kind: "reasoning", Phase: "discarded"}}
}

func (s *State) OtherItemStarted(thread string) []observation.TextUpdate { return s.drop(thread) }

func (s *State) start(update *observation.TextUpdate, at time.Time) {
	if _, ok := s.thinking[update.Ref]; ok {
		return
	}
	s.thinking[update.Ref] = at
	if p, ok := s.pending[update.Ref.ThreadID]; ok {
		delete(s.pending, update.Ref.ThreadID)
		s.thinking[update.Ref], update.Replaces = p.at, p.item
	}
}

func (s *State) TurnCompleted(thread, turn, status string, at time.Time) []observation.TextUpdate {
	s.init()
	updates := s.drop(thread)
	var keys []observation.ItemRef
	for key := range s.thinking {
		if key.ThreadID == thread {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b observation.ItemRef) int {
		return cmp.Compare(a.TurnID+"\x00"+a.ItemID, b.TurnID+"\x00"+b.ItemID)
	})
	for _, key := range keys {
		text, phase := s.reasoning[key], "item/completed"
		if strings.TrimSpace(text) == "" {
			phase = "discarded"
		}
		updates = append(updates, observation.TextUpdate{Ref: key, Kind: "reasoning", Phase: phase, Text: text, Observed: at, Thought: at.Sub(s.thinking[key]), Live: true})
		s.reasoning[key] = ""
		delete(s.thinking, key)
	}
	if last, ok := s.messages[thread]; ok && !s.finals[thread] && status == "completed" && last.Ref.TurnID == turn {
		last.Kind, last.Phase = "final", "item/completed"
		updates = append(updates, last)
	}
	delete(s.messages, thread)
	return updates
}

func (s *State) reasoningPart(e observation.TextEvent) {
	s.init()
	if _, seen := s.reasoning[e.Item.Ref]; seen {
		if _, active := s.thinking[e.Item.Ref]; !active {
			return
		}
	}
	if text := s.reasoning[e.Item.Ref]; text != "" {
		s.reasoning[e.Item.Ref] = text + "\n\n"
	}
}

func (s *State) reasoningDelta(e observation.TextEvent) []observation.TextUpdate {
	s.init()
	if _, seen := s.reasoning[e.Item.Ref]; seen {
		if _, active := s.thinking[e.Item.Ref]; !active {
			return nil
		}
	}
	s.reasoning[e.Item.Ref] += e.Delta
	u := observation.TextUpdate{Ref: e.Item.Ref, Kind: "reasoning", Phase: "summary", Text: s.reasoning[e.Item.Ref], Observed: e.At, Live: true}
	s.start(&u, e.At)
	return []observation.TextUpdate{u}
}

// Observe reduces one admitted public text notification. The application retains
// its original envelope for control and enriches user text after this projection.
func (s *State) Observe(e observation.TextEvent, main bool) []observation.TextUpdate {
	switch e.Kind {
	case observation.ItemStarted, observation.ItemCompleted:
		return s.item(e, main, e.Kind == observation.ItemCompleted)
	case observation.AgentDelta:
		updates := s.drop(e.Item.Ref.ThreadID)
		if main {
			updates = append(updates, observation.TextUpdate{Ref: e.Item.Ref, Kind: "text", Phase: string(e.Kind), Text: e.Delta, Observed: e.At, Live: true})
		}
		return updates
	case observation.ReasoningDelta:
		return s.reasoningDelta(e)
	case observation.ReasoningPart:
		s.reasoningPart(e)
	}
	return nil
}

func (s *State) item(e observation.TextEvent, main, completed bool) []observation.TextUpdate {
	s.init()
	item := e.Item
	u := observation.TextUpdate{Ref: item.Ref, Kind: "text", Phase: string(e.Kind), Text: item.Text, Status: item.Status, Observed: e.At, Live: true}
	var updates []observation.TextUpdate
	if !completed && item.Type != "reasoning" && item.Type != "userMessage" {
		updates = s.drop(item.Ref.ThreadID)
	}
	switch item.Type {
	case "reasoning":
		u.Kind, u.Text = "reasoning", strings.Join(item.Summary, "\n\n")
		if !completed {
			s.start(&u, e.At)
		} else {
			if p, ok := s.pending[item.Ref.ThreadID]; ok && s.thinking[item.Ref].IsZero() && strings.TrimSpace(u.Text) != "" {
				delete(s.pending, item.Ref.ThreadID)
				s.thinking[item.Ref], u.Replaces = p.at, p.item
			}
			if at, ok := s.thinking[item.Ref]; ok {
				delete(s.thinking, item.Ref)
				u.Thought = e.At.Sub(at)
				if strings.TrimSpace(u.Text) == "" {
					u.Text = s.reasoning[item.Ref]
				}
			}
			if strings.TrimSpace(u.Text) == "" {
				u.Phase = "discarded"
			}
		}
		s.reasoning[item.Ref] = u.Text
		if completed || strings.TrimSpace(u.Text) != "" || u.Replaces != "" {
			updates = append(updates, u)
		}
		if completed {
			s.reasoning[item.Ref] = ""
		}
	case "agentMessage":
		if !main && !completed {
			return updates
		}
		u.Kind = messageKind(item, main)
		if u.Kind == "" {
			return updates
		}
		if !main {
			u.Phase = "message"
			if u.Kind == "final" {
				u.Phase = "item/completed"
				s.finals[item.Ref.ThreadID] = true
			}
			s.messages[item.Ref.ThreadID] = u
		}
		updates = append(updates, u)
	case "userMessage":
		if main {
			updates = append(updates, u)
		}
	}
	return updates
}

func messageKind(item observation.TextItem, main bool) string {
	if item.AsyncQuestion || !main && strings.TrimSpace(item.Text) == "" {
		return ""
	}
	if !main && item.Final {
		return "final"
	}
	return "text"
}

// History projects one retained turn without reading or changing active state.
// Older pages cannot promote commentary whose later items are not in the page.
func History(items []observation.TextEvent, main, promote bool) map[string]observation.TextUpdate {
	result := make(map[string]observation.TextUpdate)
	last := ""
	hasFinal := false
	for _, e := range items {
		i := e.Item
		u := observation.TextUpdate{Ref: i.Ref, Kind: "text", Phase: string(e.Kind), Text: i.Text, Status: i.Status, Observed: e.At}
		switch i.Type {
		case "reasoning":
			u.Kind, u.Text = "reasoning", strings.Join(i.Summary, "\n\n")
			if strings.TrimSpace(u.Text) == "" {
				continue
			}
		case "agentMessage":
			u.Kind = messageKind(i, main)
			if u.Kind == "" {
				continue
			}
			hasFinal = hasFinal || u.Kind == "final"
			last = i.Ref.ItemID
		case "userMessage":
			if !main {
				continue
			}
		default:
			continue
		}
		result[i.Ref.ItemID] = u
	}
	if !main && promote && !hasFinal && last != "" {
		u := result[last]
		u.Kind = "final"
		result[last] = u
	}
	return result
}
