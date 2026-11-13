package launch

import (
	"bufio"
	"bytes"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"

	jsonv1 "encoding/json"

	json "encoding/json/v2"
	"io"
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Bound restoration I/O per thread independently of transcript size. Older
// records beyond it keep only what the host's history already shows.
const restoredRolloutLimit = 64 << 20

// restoredRollout is observational evidence that Codex's thread history
// omits: exec cell results and retained attributed deliveries. Live
// Activity reads the same records from provider input, so restoration
// projects them the same way instead of replaying any effect.
type restoredRollout struct {
	itemAt map[string]time.Time // host item completion times, by item ID
	events []restoredRolloutEvent
}

type restoredRolloutEvent struct {
	turn, anchor string // anchor: the host item completed just before it in the turn
	at           time.Time
	entry        sessionpresentation.ActivityEntry
}

// readRestoredRollout restores timestamps, execution failures and attributed deliveries.
func readRestoredRollout(info binding.ThreadInfo, agent string) restoredRollout {
	r := restoredRollout{itemAt: make(map[string]time.Time)}
	f, stat, ok := rollout.Open(info.ID, info.Path)
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
	cells := make(map[string]bool)
	for {
		// A writer's unfinished final record has no newline and is ignored.
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return r
		}
		if header := line[:min(len(line), 160)]; bytes.Contains(header, []byte(`"token_count"`)) || bytes.Contains(header, []byte(`"token_usage_record"`)) {
			continue // Usage records are the most frequent and never read here.
		}
		var record struct {
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Metadata  struct {
				ClientAuthored    bool                              `json:"client_authored"`
				Inherited         bool                              `json:"inherited_user_message"`
				Coordination      *appserver.CoordinationProjection `json:"coordination"`
				SourceAcquisition *appserver.SourceAcquisition      `json:"source_acquisition"`
			} `json:"metadata"`
			// Only the fields read here; others are skipped without copying.
			Payload struct {
				sessioncontrol.NativeMessage
				sessioncontrol.ContextReset
				ThreadID    string                            `json:"threadId"`
				EventTurnID string                            `json:"turnId"`
				Projection  *appserver.CoordinationProjection `json:"projection"`
				TurnID      string                            `json:"turn_id"`
				Item        struct {
					ID string `json:"id"`
				} `json:"item"`
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				CallID    *string           `json:"call_id"`
				Output    jsonv1.RawMessage `json:"output"`
				History   []struct {
					Metadata struct {
						SourceAcquisition *appserver.SourceAcquisition `json:"source_acquisition"`
					} `json:"metadata"`
				} `json:"replacement_history"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		p := record.Payload
		// Retained-only deliveries can precede the first host turn. Their
		// timestamps place them without inventing a turn or starting work.
		event := restoredRolloutEvent{turn: turn, anchor: anchor, at: record.Timestamp}
		if record.Type == "compacted" {
			for _, item := range p.History {
				if entry, ok := sessionpresentation.NativeSourceAcquisitionEntry(info.Cwd, info.ID, turn, item.Metadata.SourceAcquisition, record.Timestamp); ok {
					delivered := event
					delivered.entry = entry
					r.events = append(r.events, delivered)
				}
			}
		}
		if record.Type == "response_item" {
			if entry, ok := sessionpresentation.NativeSourceAcquisitionEntry(info.Cwd, info.ID, turn, record.Metadata.SourceAcquisition, record.Timestamp); ok {
				delivered := event
				delivered.entry = entry
				r.events = append(r.events, delivered)
			}
			for _, entry := range sessionpresentation.NativeCoordinationEntries(info.ID, turn, p.NativeMessage, record.Metadata.Coordination, record.Timestamp, record.Metadata.Inherited) {
				delivered := event
				delivered.entry = entry
				r.events = append(r.events, delivered)
			}
		}
		switch record.Type + "/" + p.Type {
		case "event_msg/task_started", "event_msg/turn_started":
			turn, anchor = p.TurnID, ""
		case "event_msg/task_complete", "event_msg/turn_complete", "event_msg/turn_aborted":
		case "event_msg/item_completed":
			if p.Item.ID == "" {
				continue
			}
			r.itemAt[p.Item.ID] = record.Timestamp
			if p.TurnID == turn {
				anchor = p.Item.ID
			}
		case "response_item/custom_tool_call":
			if p.Name == "exec" && (p.Namespace == "" || p.Namespace == "functions") {
				if p.CallID != nil {
					cells[*p.CallID] = true
				}
			}
		case "response_item/custom_tool_call_output":
			if text, failed := binding.CodeModeFailureText(binding.ExecutionOutputTexts(p.Output)); p.CallID != nil && cells[*p.CallID] && failed && turn != "" {
				event.entry = sessionpresentation.ActivityEntry{Agent: agent, Kind: "error", Text: activitypresentation.ErrorPreview(text), ErrorDetail: text, CallID: *p.CallID}
				r.events = append(r.events, event)
			}
		case "response_item/function_call_output":
			if record.Metadata.ClientAuthored && p.CallID == nil && p.Namespace == "threadcontrol" {
				var output string
				if json.Unmarshal(p.Output, &output) == nil {
					if entry, ok := sessionpresentation.RetainedCompactionEntry(info.ID, agent, p.Name, output, record.Timestamp); ok {
						event.entry = entry
						r.events = append(r.events, event)
					}
					if entry, ok := sessionpresentation.RetainedWaitEntry(info.ID, agent, p.Name, output, record.Timestamp); ok {
						event.entry = entry
						r.events = append(r.events, event)
					}
				}
			}
		case "event_msg/context_reset":
			reset := p.ContextReset
			reset.Thread, reset.Turn = p.ThreadID, p.EventTurnID
			if reset.Thread == info.ID || record.Metadata.Inherited {
				if entry, ok := sessionpresentation.NativeContextResetEntry(reset, record.Timestamp); ok {
					event.entry = entry
					r.events = append(r.events, event)
				}
			}
		case "event_msg/agent_coordination":
			if p.ThreadID == info.ID || record.Metadata.Inherited {
				for _, entry := range sessionpresentation.NativeCoordinationEntries(info.ID, p.EventTurnID, sessioncontrol.NativeMessage{}, p.Projection, record.Timestamp, record.Metadata.Inherited) {
					delivered := event
					delivered.entry = entry
					r.events = append(r.events, delivered)
				}
			}
		}
	}
}

// restoredPlacement is a restored entry awaiting its position in one thread's
// history. Evidence from the thread's own rollout follows its anchor; another
// thread's evidence is placed by completion time.
type restoredPlacement struct {
	entry        sessionpresentation.ActivityEntry
	turn, anchor string // empty turn places by time
	at           time.Time
}

func (p *restoredPlacement) position() sessionpresentation.Placement {
	return sessionpresentation.Placement{Turn: p.turn, Anchor: p.anchor, At: p.at}
}

func historyPositions(turns []binding.HistoryTurn, itemAt map[string]time.Time) []sessionpresentation.TurnPosition {
	positions := make([]sessionpresentation.TurnPosition, len(turns))
	for i, turn := range turns {
		positions[i] = sessionpresentation.TurnPosition{ID: turn.ID, Started: historyTime(turn.StartedAt), Items: make([]sessionpresentation.ItemPosition, len(turn.Items))}
		for j, item := range turn.Items {
			at, known := itemAt[item.ID]
			positions[i].Items[j] = sessionpresentation.ItemPosition{ID: item.ID, At: at, Known: known}
		}
	}
	return positions
}

func restoredPositions(placements []*restoredPlacement) []sessionpresentation.Placement {
	positions := make([]sessionpresentation.Placement, len(placements))
	for i, placement := range placements {
		positions[i] = placement.position()
	}
	return positions
}
