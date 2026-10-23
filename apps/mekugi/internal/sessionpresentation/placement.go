package sessionpresentation

import (
	"slices"
	"time"
)

// TurnPosition and ItemPosition retain only the identities and times needed to
// place history. Content and retained evidence stay with their feature owners.
type TurnPosition struct {
	ID      string
	Started time.Time
	Items   []ItemPosition
}

type ItemPosition struct {
	ID    string
	At    time.Time
	Known bool
}

type Placement struct {
	Turn, Anchor string
	At           time.Time
}

// TurnFor selects the latest turn started by the observation, or the first
// turn. An explicit turn identity never falls back to an unrelated time slot.
// Source: apps/mekugi/internal/router/app_server_restore_rollout.go:213:225@eaad597b.
func (p Placement) TurnFor(turns []TurnPosition) int {
	if p.Turn != "" {
		return slices.IndexFunc(turns, func(t TurnPosition) bool { return t.ID == p.Turn })
	}
	index := min(0, len(turns)-1)
	for i, turn := range turns {
		begin := turn.Started
		if begin.IsZero() && len(turn.Items) > 0 {
			begin = turn.Items[0].At
		}
		if !begin.After(p.At) {
			index = i
		}
	}
	return index
}

// Slots returns supplied placement indices: slot zero precedes the first item,
// and slot i+1 follows item i. Equal-time evidence retains arrival order.
// Source: apps/mekugi/internal/router/app_server_restore_rollout.go:179:209@eaad597b.
func (t TurnPosition) Slots(placements []Placement) [][]int {
	slots := make([][]int, len(t.Items)+1)
	times := make([]time.Time, len(t.Items))
	at := t.Started
	for i, item := range t.Items {
		if item.Known {
			at = item.At
		}
		times[i] = at
	}
	for i, p := range placements {
		slot := 0
		if p.Turn != "" {
			slot = slices.IndexFunc(t.Items, func(item ItemPosition) bool { return item.ID == p.Anchor }) + 1
		}
		if p.Turn == "" || p.Anchor != "" && slot == 0 {
			for j, completed := range times {
				if !completed.After(p.At) {
					slot = j + 1
				}
			}
		}
		slots[slot] = append(slots[slot], i)
	}
	for _, slot := range slots {
		slices.SortStableFunc(slot, func(a, b int) int { return placements[a].At.Compare(placements[b].At) })
	}
	return slots
}
