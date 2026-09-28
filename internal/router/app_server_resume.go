package router

import (
	json "encoding/json/v2"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

func (u *appServerUI) requestResume() error {
	u.status = "Resuming thread…"
	return u.request("thread/resume", map[string]any{"threadId": u.resumeThread, "approvalPolicy": "never", "sandbox": "danger-full-access", "config": u.resumeConfig, "modelProvider": u.resumeConfig["model_provider"]})
}

func (u *appServerUI) resumeLastResponse(m appserver.Message) error {
	if m.Error != nil {
		return fmt.Errorf("find latest thread: %s", m.Error.Message)
	}
	var result struct {
		Data []appServerThreadInfo `json:"data"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		return fmt.Errorf("find latest thread: %w", err)
	}
	if len(result.Data) == 0 {
		return fmt.Errorf("no resumable thread found for %s", u.resumeCwd)
	}
	if result.Data[0].ID == "" {
		return fmt.Errorf("thread/list returned no thread identity")
	}
	u.resumeThread = result.Data[0].ID
	return u.requestResume()
}

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
			if item.Type == "userMessage" {
				var content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
					Name string `json:"name"`
					Path string `json:"path"`
				}
				if json.Unmarshal(item.Content, &content) == nil {
					var text strings.Builder
					for _, part := range content {
						if part.Type == "text" {
							text.WriteString(part.Text)
						}
					}
					draft := composerDraft{text: text.String()}
					paths := make(map[string]string)
					for _, part := range content {
						if part.Type == "skill" {
							if previous, ok := paths[part.Name]; ok && previous != part.Path {
								paths[part.Name] = ""
							} else if !ok {
								paths[part.Name] = part.Path
							}
						}
					}
					for name, path := range paths {
						if path != "" {
							draft.skills = append(draft.skills, restoredSkillBindings(draft.text, name, path)...)
						}
					}
					u.rememberInput(draft)
				}
			}
			method := "item/completed"
			if turn.Status == "inProgress" && (item.Type == "agentMessage" || item.Status == "inProgress") {
				method = "item/started"
			}
			if _, progress := appServerProgress(item, method); progress {
				method = appServerHistoryProgressPhase(item)
			}
			switch item.Type {
			case "imageView":
				entry := activityPaneEntry{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "tool", Text: "View " + commentaryCode(pathdisplay.ForWorkspace(u.session.cwd, item.Path)), CallID: item.ID, Observed: time.Now(),
					native: &liveActivityNativeItem{thread: u.thread, turn: turn.ID, item: item.ID, phase: method, searchResults: appServerSearchResults(item)}}
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
			case "commandExecution", "fileChange", "webSearch":
				if u.internalJournalCommand(u.thread, item) {
					continue
				}
				text := appServerToolText(item, u.session.cwd)
				if item.Type == "fileChange" {
					text = appServerEditText(item, u.session.cwd)
				}
				entry := activityPaneEntry{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "tool", Text: text, CallID: item.ID, Observed: time.Now(),
					native: &liveActivityNativeItem{thread: u.thread, turn: turn.ID, item: item.ID, phase: method, searchResults: appServerSearchResults(item)}}
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
				if item.ExitCode != nil && *item.ExitCode != 0 {
					exit := activityPaneEntry{Seq: u.view.lastSeq + 1, Agent: "Main", Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID}
					exit.outputTail, exit.outputOmit = appServerOutputTail(item.AggregatedOutput)
					u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{exit}})
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
