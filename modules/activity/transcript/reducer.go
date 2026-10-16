// Package transcript reduces admitted text facts into the existing Activity feed.
package transcript

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
)

type pending struct {
	item string
	at   time.Time
}

type threadState struct {
	pending  pending
	thinking int
}

type turnState struct {
	reasoning map[string]string // Active bodies; completed items retain empty tombstones.
	thinking  map[string]time.Time
	message   observation.TextUpdate
	final     bool
}

// State owns attachment sequencing and private keys into application-owned
// thread and turn stores. Feed records and outputs remain with their owners.
// Source: apps/mekugi/internal/router/app_server_events.go:567:637@[453772ac].
type State struct {
	thread   scope.Key[threadState]
	turn     scope.Key[turnState]
	sequence uint64
}

func (t *turnState) initReasoning() {
	if t.reasoning == nil {
		t.reasoning = make(map[string]string)
		t.thinking = make(map[string]time.Time)
	}
}

func (s *State) ProviderStarted(thread string, at time.Time, store *scope.Store) []observation.TextUpdate {
	t := s.thread.Value(store)
	if t.thinking != 0 {
		return nil
	}
	updates := t.drop(thread)
	s.sequence++
	item := "thinking:" + strconv.FormatUint(s.sequence, 10)
	t.pending = pending{item, at}
	return append(updates, observation.TextUpdate{Ref: observation.ItemRef{ThreadID: thread, ItemID: item}, Kind: "reasoning", Phase: "summary", Observed: at, Live: true})
}

func (t *threadState) drop(thread string) []observation.TextUpdate {
	p := t.pending
	if p.item == "" {
		return nil
	}
	t.pending = pending{}
	return []observation.TextUpdate{{Ref: observation.ItemRef{ThreadID: thread, ItemID: p.item}, Kind: "reasoning", Phase: "discarded"}}
}

func (s *State) OtherItemStarted(thread string, store *scope.Store) []observation.TextUpdate {
	return s.thread.Value(store).drop(thread)
}

func (t *turnState) start(update *observation.TextUpdate, at time.Time, thread *threadState) {
	t.initReasoning()
	id := update.Ref.ItemID
	if _, ok := t.thinking[id]; ok {
		return
	}
	t.thinking[id] = at
	thread.thinking++
	if p := thread.pending; p.item != "" {
		thread.pending = pending{}
		t.thinking[id], update.Replaces = p.at, p.item
	}
}

func (s *State) TurnCompleted(thread, turn, status string, at time.Time, current bool, stores observation.TextScope) []observation.TextUpdate {
	th, t := s.thread.Value(stores.Thread), s.turn.Value(stores.Turn)
	var updates []observation.TextUpdate
	if current {
		updates = th.drop(thread)
	}
	var keys []string
	for key := range t.thinking {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		text, phase := t.reasoning[key], "item/completed"
		if strings.TrimSpace(text) == "" {
			phase = "discarded"
		}
		updates = append(updates, observation.TextUpdate{Ref: observation.ItemRef{ThreadID: thread, TurnID: turn, ItemID: key}, Kind: "reasoning", Phase: phase, Text: text, Observed: at, Thought: at.Sub(t.thinking[key]), Live: true})
		t.reasoning[key] = ""
		delete(t.thinking, key)
		th.thinking--
	}
	if current && t.message.Kind != "" {
		if !t.final && status == "completed" {
			last := t.message
			last.Kind, last.Phase = "final", "item/completed"
			updates = append(updates, last)
		}
		t.message = observation.TextUpdate{}
	}
	return updates
}

func (t *turnState) reasoningPart(e observation.TextEvent) {
	id := e.Item.Ref.ItemID
	if _, seen := t.reasoning[id]; seen {
		if _, active := t.thinking[id]; !active {
			return
		}
	}
	if text := t.reasoning[id]; text != "" {
		t.reasoning[id] = text + "\n\n"
	}
}

func (t *turnState) reasoningDelta(e observation.TextEvent, thread *threadState) []observation.TextUpdate {
	t.initReasoning()
	id := e.Item.Ref.ItemID
	if _, seen := t.reasoning[id]; seen {
		if _, active := t.thinking[id]; !active {
			return nil
		}
	}
	t.reasoning[id] += e.Delta
	u := observation.TextUpdate{Ref: e.Item.Ref, Kind: "reasoning", Phase: "summary", Text: t.reasoning[id], Observed: e.At, Live: true}
	t.start(&u, e.At, thread)
	return []observation.TextUpdate{u}
}

// Observe reduces one admitted public text notification. The application retains
// its original envelope for control and enriches user text after this projection.
func (s *State) Observe(e observation.TextEvent, main bool, stores observation.TextScope) []observation.TextUpdate {
	var th *threadState
	var t *turnState
	if stores.Turn == nil {
		if e.Kind != observation.ItemCompleted {
			return nil
		}
		// Late authoritative completion updates its original feed item without
		// touching active correlation or another provider request's placeholder.
		th, t = new(threadState), new(turnState)
	} else {
		th, t = s.thread.Value(stores.Thread), s.turn.Value(stores.Turn)
	}
	switch e.Kind {
	case observation.ItemStarted, observation.ItemCompleted:
		return t.item(e, main, e.Kind == observation.ItemCompleted, th)
	case observation.AgentDelta:
		updates := th.drop(e.Item.Ref.ThreadID)
		if main {
			updates = append(updates, observation.TextUpdate{Ref: e.Item.Ref, Kind: "text", Phase: string(e.Kind), Text: e.Delta, Observed: e.At, Live: true})
		}
		return updates
	case observation.ReasoningDelta:
		return t.reasoningDelta(e, th)
	case observation.ReasoningPart:
		t.reasoningPart(e)
	}
	return nil
}

func (t *turnState) item(e observation.TextEvent, main, completed bool, thread *threadState) []observation.TextUpdate {
	item := e.Item
	id := item.Ref.ItemID
	u := observation.TextUpdate{Ref: item.Ref, Kind: "text", Phase: string(e.Kind), Text: item.Text, Status: item.Status, Observed: e.At, Live: true}
	var updates []observation.TextUpdate
	if !completed && item.Type != "reasoning" && item.Type != "userMessage" {
		updates = thread.drop(item.Ref.ThreadID)
	}
	switch item.Type {
	case "reasoning":
		t.initReasoning()
		u.Kind, u.Text = "reasoning", strings.Join(item.Summary, "\n\n")
		if !completed {
			t.start(&u, e.At, thread)
		} else {
			_, seen := t.reasoning[id]
			if p := thread.pending; !seen && p.item != "" && strings.TrimSpace(u.Text) != "" {
				thread.pending = pending{}
				u.Thought, u.Replaces = e.At.Sub(p.at), p.item
			}
			if at, ok := t.thinking[id]; ok {
				delete(t.thinking, id)
				thread.thinking--
				u.Thought = e.At.Sub(at)
				if strings.TrimSpace(u.Text) == "" {
					u.Text = t.reasoning[id]
				}
			} else if strings.TrimSpace(u.Text) == "" {
				t.reasoning[id] = ""
				return nil
			}
			if strings.TrimSpace(u.Text) == "" {
				u.Phase = "discarded"
			}
		}
		t.reasoning[id] = u.Text
		if completed || strings.TrimSpace(u.Text) != "" || u.Replaces != "" {
			updates = append(updates, u)
		}
		if completed {
			t.reasoning[id] = ""
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
				t.final = true
			}
			t.message = u
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
