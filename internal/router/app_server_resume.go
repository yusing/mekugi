package router

import (
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Codex merges saved model metadata after loading process CLI configuration.
// Forward explicit model settings on resume as well so that invocation-local
// overrides retain their meaning. Other config keeps its app-server owner.
func appServerResumeConfig(args []string) map[string]any {
	config := make(map[string]any)
	for i := 0; i < len(args); i++ {
		if args[i] != "-c" || i+1 == len(args) {
			continue
		}
		i++
		key, value, ok := strings.Cut(args[i], "=")
		key = strings.TrimSpace(key)
		if !ok || (key != "model" && key != "model_provider" && key != "model_reasoning_effort") {
			continue
		}
		var parsed map[string]any
		if _, err := toml.Decode("value = "+value, &parsed); err == nil {
			config[key] = parsed["value"]
		} else {
			// Codex accepts bare strings when a CLI value is not valid TOML.
			config[key] = strings.TrimSpace(value)
		}
	}
	return config
}

type appServerHistoryTurn struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Items       []appServerItem `json:"items"`
	StartedAt   int64           `json:"startedAt"`
	CompletedAt int64           `json:"completedAt"`
	Error       *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// History is presentation evidence, not a stream of live lifecycle events.
// In particular, old edits and collaboration calls must not revive previews,
// processes, child agents, or journal delivery receipts.
func (u *appServerUI) restoreHistory(turns []appServerHistoryTurn) {
	for _, turn := range turns {
		for _, item := range turn.Items {
			method := "item/completed"
			if turn.Status == "inProgress" && (item.Type == "agentMessage" || item.Status == "inProgress") {
				method = "item/started"
			}
			switch item.Type {
			case "commandExecution", "fileChange":
				text := appServerCommandText(item, u.session.cwd)
				if item.Type == "fileChange" {
					text = appServerEditText(item, u.session.cwd)
				}
				entry := activityPaneEntry{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "tool", Text: text, CallID: item.ID, Observed: time.Now(),
					native: &liveActivityNativeItem{thread: u.thread, turn: turn.ID, item: item.ID, phase: method}}
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
				if item.ExitCode != nil && *item.ExitCode != 0 {
					u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID}}})
				}
			default:
				u.view.applyAppServerItem(u.thread, u.thread, turn.ID, item.ID, method, "", item)
			}
		}
		if turn.Status == "inProgress" {
			u.turn, u.status, u.turnStarted = turn.ID, "Working", time.Now()
		}
	}
	root := u.session.agent("/root")
	root.Turns = uint64(len(turns))
	root.Responding = u.turn != ""
	// Keep future activity sequence numbers ahead of the hydrated transcript.
	u.session.seq = u.view.lastSeq
	u.applyActivity(nil, u.session.agents)
}
