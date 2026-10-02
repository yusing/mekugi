package router

import (
	"bufio"
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"io"
	"slices"
	"strings"
	"time"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Bound restoration I/O per thread independently of transcript size. Older
// records beyond it keep only what the host's history already shows.
const restoredRolloutLimit = 64 << 20

// restoredRollout is observational evidence that Codex's thread history
// omits: Code Mode cell results and delivered inter-agent messages. Live
// Activity reads the same records from provider input, so restoration
// projects them the same way instead of replaying any effect.
type restoredRollout struct {
	itemAt map[string]time.Time // host item completion times, by item ID
	turnAt map[string]journalReplayTurn
	events []restoredRolloutEvent
}

type restoredRolloutEvent struct {
	turn, anchor string // anchor: the host item completed just before it in the turn
	at           time.Time
	entry        activityPaneEntry
	// repeated evidence reaches the thread's later live requests again: every
	// task, and messages after the input's last user, assistant, reasoning or
	// call item, which live Activity reads as newly delivered.
	repeated bool
}

// readRestoredRollout projects one thread's retained rollout. agent is the
// thread's canonical path, which addressed messages must name exactly.
func readRestoredRollout(info appServerThreadInfo, agent string) restoredRollout {
	r := restoredRollout{itemAt: make(map[string]time.Time), turnAt: make(map[string]journalReplayTurn)}
	f, stat, ok := openThreadRollout(info)
	if !ok {
		return r
	}
	defer f.Close()
	start := max(0, stat.Size()-restoredRolloutLimit)
	reader := bufio.NewReaderSize(io.NewSectionReader(f, start, stat.Size()-start), 64<<10)
	if start > 0 {
		// Skip the record the bound cut through.
		if _, err := reader.ReadBytes('\n'); err != nil {
			return r
		}
	}
	var turn, anchor string
	var settings *activityStart
	// A cut-off history cannot tell which task started the child.
	started := start > 0
	cells := make(map[string]bool)
	delivered := 0 // Events before this were followed by consuming input.
	for {
		// A writer's unfinished final record has no newline and is ignored.
		line, err := reader.ReadBytes('\n')
		if err != nil {
			for i := delivered; i < len(r.events); i++ {
				r.events[i].repeated = r.events[i].repeated || r.events[i].entry.Kind == "reply"
			}
			return r
		}
		if header := line[:min(len(line), 160)]; bytes.Contains(header, []byte(`"token_count"`)) || bytes.Contains(header, []byte(`"token_usage_record"`)) {
			continue // Usage records are the most frequent and never read here.
		}
		var record struct {
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			// Only the fields read here; others are skipped without copying.
			Payload struct {
				Type   string `json:"type"`
				TurnID string `json:"turn_id"`
				Item   struct {
					ID string `json:"id"`
				} `json:"item"`
				Name        string            `json:"name"`
				Namespace   string            `json:"namespace"`
				CallID      string            `json:"call_id"`
				Output      jsonv1.RawMessage `json:"output"`
				Recipient   string            `json:"recipient"`
				Role        string            `json:"role"`
				Model       string            `json:"model"`
				Effort      *string           `json:"effort"`
				ServiceTier *string           `json:"service_tier"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		p := record.Payload
		// Evidence outside a known turn has no position in the host's history.
		event := restoredRolloutEvent{turn: turn, anchor: anchor, at: record.Timestamp}
		if record.Type == "response_item" && (p.Type == "message" && (p.Role == "user" || p.Role == "assistant") || p.Type == "reasoning" || strings.HasSuffix(p.Type, "_call")) {
			delivered = len(r.events)
		}
		switch record.Type + "/" + p.Type {
		case "event_msg/task_started", "event_msg/turn_started":
			turn, anchor = p.TurnID, ""
			window := r.turnAt[p.TurnID]
			window.id, window.start = p.TurnID, record.Timestamp
			r.turnAt[p.TurnID] = window
		case "event_msg/task_complete", "event_msg/turn_complete", "event_msg/turn_aborted":
			window := r.turnAt[p.TurnID]
			window.end = record.Timestamp
			r.turnAt[p.TurnID] = window
		case "event_msg/item_completed":
			if p.Item.ID == "" {
				continue
			}
			r.itemAt[p.Item.ID] = record.Timestamp
			if p.TurnID == turn {
				anchor = p.Item.ID
			}
		case "turn_context/":
			settings = subagentStart(p.Model, deref(p.Effort), deref(p.ServiceTier))
		case "response_item/custom_tool_call":
			if p.Name == "exec" && (p.Namespace == "" || p.Namespace == "functions") {
				cells[p.CallID] = true
			}
		case "response_item/custom_tool_call_output":
			if text, failed := codeModeFailureText(executionOutputTexts(p.Output)); cells[p.CallID] && failed && turn != "" {
				event.entry = activityPaneEntry{Agent: agent, Kind: "error", Text: activityui.ErrorPreview(text), ErrorDetail: text, CallID: p.CallID}
				r.events = append(r.events, event)
			}
		case "response_item/agent_message":
			var message struct {
				Payload map[string]jsonv1.RawMessage `json:"payload"`
			}
			if p.Recipient != agent || turn == "" || json.Unmarshal(line, &message) != nil {
				continue
			}
			p := message.Payload
			sender := jsonString(p, "author")
			if text, ok := journalAssignmentText(p, agent); ok {
				if strings.TrimSpace(text) == "" {
					continue // Opaque tasks never become an inferred plaintext assignment.
				}
				assignment := &activityAssignment{id: subagentCommentaryMessageID("assignment\x00" + jsonString(p, "id") + "\x00" + sender + "\x00" + agent + "\x00" + text),
					from: sender, to: agent, text: text, created: record.Timestamp}
				event.entry = activityPaneEntry{Agent: agent, Kind: "assignment", Text: text, assignment: assignment}
				event.repeated = true
				if !started && settings != nil {
					// As live, the first task arrives with the child's start,
					// which needs the model it ran with.
					event.entry.Kind, event.entry.Text, event.entry.start = "start", "", settings
				}
				started = true
				r.events = append(r.events, event)
			} else if text, sender, final, ok := subagentResponse(p); ok && !final {
				event.entry = activityPaneEntry{Agent: agent, Kind: "reply", Text: text, message: &activityMessage{from: sender, to: agent, text: text}}
				r.events = append(r.events, event)
			}
		}
	}
}

// restoredPlacement is a restored entry awaiting its position in one thread's
// history. Evidence from the thread's own rollout follows its anchor; another
// thread's evidence is placed by completion time.
type restoredPlacement struct {
	entry        activityPaneEntry
	turn, anchor string // empty turn places by time
	at           time.Time
	// link is the Activity entry this Main copy excerpts, once applied.
	link *uint64
}

// placeRestored splits placements among a turn's history items: slot 0
// precedes the first item and slot i+1 follows item i.
func placeRestored(turn appServerHistoryTurn, itemAt map[string]time.Time, placements []*restoredPlacement) [][]*restoredPlacement {
	slots := make([][]*restoredPlacement, len(turn.Items)+1)
	times := make([]time.Time, len(turn.Items))
	at := historyTime(turn.StartedAt)
	for i, item := range turn.Items {
		if completed, ok := itemAt[item.ID]; ok {
			at = completed
		}
		times[i] = at
	}
	for _, p := range placements {
		slot := 0
		if p.turn != "" {
			slot = slices.IndexFunc(turn.Items, func(item appServerItem) bool { return item.ID == p.anchor }) + 1
		}
		if p.turn == "" || p.anchor != "" && slot == 0 {
			// Without its anchor in the host's history, time orders it.
			for i, completed := range times {
				if !completed.After(p.at) {
					slot = i + 1
				}
			}
		}
		slots[slot] = append(slots[slot], p)
	}
	for _, slot := range slots {
		// Evidence from several threads can follow one item.
		slices.SortStableFunc(slot, func(a, b *restoredPlacement) int { return a.at.Compare(b.at) })
	}
	return slots
}

// restoredTurnFor selects the turn a time-placed entry belongs to: the last
// one started by then, else the first. It returns -1 without turns.
func restoredTurnFor(turns []appServerHistoryTurn, itemAt map[string]time.Time, at time.Time) int {
	index := min(0, len(turns)-1)
	for i, turn := range turns {
		begin := historyTime(turn.StartedAt)
		if begin.IsZero() && len(turn.Items) > 0 {
			begin = itemAt[turn.Items[0].ID]
		}
		if !begin.After(at) {
			index = i
		}
	}
	return index
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
