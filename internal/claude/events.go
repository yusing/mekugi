package claude

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/internal/session"
)

type adapter struct {
	sides         map[string]*adapter
	resumeSession string
	streams       map[string]string
	text          map[string]*textMessage
	tools         map[toolBlock]*toolInput
	historyShell  *session.ShellCommand
}

func (a *adapter) decode(data []byte) (events []session.Event, err error) {
	var frame struct {
		CommandInfo []session.Command      `json:"commandInfo"`
		Models      []session.Model        `json:"models"`
		Field       string                 `json:"field"`
		Value       string                 `json:"value"`
		Failed      bool                   `json:"failed"`
		Kind        string                 `json:"kind"`
		SessionID   string                 `json:"sessionID"`
		AgentID     string                 `json:"agentID"`
		Title       string                 `json:"title"`
		Cwd         string                 `json:"cwd"`
		Sessions    []session.SavedSession `json:"sessions"`
		Cursor      string                 `json:"cursor"`
		ID          string                 `json:"id"`
		Tool        string                 `json:"tool"`
		Text        string                 `json:"text"`
		Description string                 `json:"description"`
		Caller      string                 `json:"caller"`
		Callers     []string               `json:"callers"`
		TaskID      string                 `json:"taskID"`
		OutputFile  string                 `json:"outputFile"`
		Truncated   bool                   `json:"truncated"`
		Done        bool                   `json:"done"`
		Command     string                 `json:"command"`
		Output      string                 `json:"output"`
		Retained    bool                   `json:"retained"`
		Input       jsontext.Value         `json:"input"`
		Event       nativeEvent            `json:"event"`
		Frame       jsontext.Value         `json:"frame"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, fmt.Errorf("invalid Claude bridge frame: %w", err)
	}
	defer func() {
		for i := range events {
			if events[i].Caller == "" {
				events[i].Caller = frame.Event.Parent
			}
		}
	}()
	switch frame.Kind {
	case "side":
		if frame.ID == "" {
			return nil, fmt.Errorf("missing native side identity")
		}
		var header struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(frame.Frame, &header); err != nil {
			return nil, err
		}
		if header.Kind != "event" && header.Kind != "error" && header.Kind != "side_closed" {
			return nil, fmt.Errorf("invalid native side frame")
		}
		if header.Kind == "side_closed" {
			delete(a.sides, frame.ID)
			return []session.Event{{Kind: "side_closed", SideID: frame.ID}}, nil
		}
		if header.Kind == "error" {
			events, err := new(adapter).decode(frame.Frame)
			for i := range events {
				events[i].SideID = frame.ID
			}
			return events, err
		}
		if a.sides == nil {
			a.sides = make(map[string]*adapter)
		}
		if a.sides[frame.ID] == nil {
			a.sides[frame.ID] = new(adapter)
		}
		events, err := a.sides[frame.ID].decode(frame.Frame)
		for i := range events {
			events[i].SideID = frame.ID
		}
		return events, err
	case "session":
		if frame.SessionID == "" {
			return nil, fmt.Errorf("native resume identity unavailable")
		}
		a.resumeSession = frame.SessionID
		e := session.Event{Kind: "session", SessionID: frame.SessionID, Cwd: frame.Cwd}
		if frame.Title != "" {
			e.Title = &session.SessionTitle{SessionID: frame.SessionID, Title: frame.Title}
		}
		return []session.Event{e}, nil
	case "ready":
		return []session.Event{{Kind: "ready", CommandInfo: frame.CommandInfo, Models: frame.Models}}, nil
	case "commands":
		return []session.Event{{Kind: "commands", CommandInfo: frame.CommandInfo}}, nil
	case "shell_restarting", "shell_restarted":
		return []session.Event{{Kind: frame.Kind, CommandInfo: frame.CommandInfo, Text: frame.Text}}, nil
	case "settings":
		return []session.Event{{Kind: "settings", Settings: &session.Settings{ID: frame.ID, Field: frame.Field, Value: frame.Value}, Failed: frame.Failed, Text: frame.Text}}, nil
	case "title":
		return []session.Event{{Kind: "title", Title: &session.SessionTitle{ID: frame.ID, SessionID: frame.SessionID, Title: frame.Title}, Failed: frame.Failed, Text: frame.Text}}, nil
	case "sessions":
		return []session.Event{{Kind: frame.Kind, Sessions: &session.SessionPage{ID: frame.ID, Cursor: frame.Cursor, Sessions: frame.Sessions}, Failed: frame.Failed, Text: frame.Text}}, nil
	case "session_change", "session_ready":
		if frame.Kind == "session_change" {
			a.resumeSession, a.streams, a.text, a.tools = frame.SessionID, nil, nil, nil
		}
		return []session.Event{{Kind: frame.Kind, Change: &session.SessionChange{ID: frame.ID, SessionID: frame.SessionID, Title: frame.Title, Cwd: frame.Cwd}, Failed: frame.Failed, Text: frame.Text}}, nil
	case "task_control":
		return []session.Event{{Kind: "task_control", ID: frame.ID, Failed: frame.Failed, Text: frame.Text}}, nil
	case "agent_message":
		return []session.Event{{Kind: frame.Kind, AgentMessage: &session.AgentMessage{ID: frame.ID, SessionID: frame.SessionID, AgentID: frame.AgentID, Text: frame.Text}, Failed: frame.Failed}}, nil
	case "shell_started", "shell_done":
		result := &session.ShellResult{ShellCommand: session.ShellCommand{ID: frame.ID, SessionID: frame.SessionID, Command: frame.Command}, Retained: frame.Retained}
		result.Output, result.ExitCode = shellOutput(frame.Output)
		events := []session.Event{{Kind: frame.Kind, Shell: result, Failed: frame.Failed, Text: frame.Text}}
		if frame.Kind == "shell_started" {
			events = append([]session.Event{{Kind: "session", SessionID: frame.SessionID, Cwd: frame.Cwd}}, events...)
		}
		return events, nil
	case "saved_agent":
		return []session.Event{{Kind: "task", Historical: true, Callers: frame.Callers, Task: &session.Task{ID: frame.ID, Kind: "local_agent", Status: "saved"}}}, nil
	case "reset":
		if frame.Failed || frame.ID == "" || frame.SessionID == "" {
			return nil, fmt.Errorf("validated native reset identity unavailable")
		}
		a.resumeSession = frame.SessionID
		return []session.Event{{Kind: frame.Kind, ID: frame.ID, SessionID: frame.SessionID}}, nil
	case "reset_ready":
		return []session.Event{{Kind: frame.Kind, ID: frame.ID, Failed: frame.Failed, Text: frame.Text, SessionID: frame.SessionID}}, nil
	case "history":
		if events, ok := a.shellHistory(frame.Event); ok {
			return events, nil
		}
		return historyEvents(frame.Event)
	case "notice":
		return []session.Event{{Kind: "notice", Text: frame.Text}}, nil
	case "command_output":
		if frame.ID == "" || frame.TaskID == "" || len(frame.Text) > 16<<10 {
			return nil, fmt.Errorf("invalid native command output snapshot")
		}
		if frame.OutputFile != "" && (!frame.Done || frame.Text != "" || !filepath.IsAbs(frame.OutputFile)) {
			return nil, fmt.Errorf("invalid native command output file")
		}
		return []session.Event{{Kind: "command_output", ID: frame.ID, Caller: frame.Caller, Text: frame.Text, Failed: frame.Failed, Output: &session.CommandOutput{TaskID: frame.TaskID, OutputFile: frame.OutputFile, Truncated: frame.Truncated, Done: frame.Done}}}, nil
	case "error":
		return []session.Event{{Kind: "error", Text: frame.Text}}, nil
	case "permission_cancelled":
		return []session.Event{{Kind: "dismiss", ID: frame.ID}}, nil
	case "permission":
		prompt := &session.Prompt{ID: frame.ID, Tool: frame.Tool, Description: frame.Description}
		if frame.Tool == "AskUserQuestion" {
			var input struct {
				Questions []struct {
					Question    string `json:"question"`
					Header      string `json:"header"`
					MultiSelect bool   `json:"multiSelect"`
					Options     []struct {
						Label       string `json:"label"`
						Description string `json:"description"`
					} `json:"options"`
				} `json:"questions"`
			}
			if err := json.Unmarshal(frame.Input, &input); err != nil {
				return nil, err
			}
			for _, q := range input.Questions {
				question := session.Question{Text: q.Question, Header: q.Header, Multiple: q.MultiSelect}
				for _, o := range q.Options {
					question.Options = append(question.Options, session.Option{Label: o.Label, Description: o.Description})
				}
				prompt.Questions = append(prompt.Questions, question)
			}
		} else {
			prompt.Description += "\n" + string(frame.Input)
		}
		return []session.Event{{Kind: "prompt", Prompt: prompt}}, nil
	case "event":
	default:
		return nil, nil
	}
	e := frame.Event
	switch e.Type {
	case "rate_limit_event":
		if e.Limit != nil {
			return []session.Event{{Kind: "limit", Limit: e.Limit}}, nil
		}
	case "system":
		if e.Subtype == "commands_changed" {
			return []session.Event{{Kind: "commands", CommandInfo: e.Commands}}, nil
		}
		if e.TaskID != "" {
			task := &session.Task{ID: e.TaskID, ToolID: e.ToolUseID, Kind: e.TaskType, Role: e.SubagentType, Description: e.Description, Summary: e.Summary, Ambient: e.Ambient || e.SkipTranscript}
			switch e.Subtype {
			case "task_started":
				task.Status = "running"
			case "task_progress":
				// Progress is not a lifecycle transition (for example unpausing).
			case "task_notification":
				task.Status = e.Status
			case "task_updated":
				task.Status, task.Description, task.Summary = e.Patch.Status, e.Patch.Description, e.Patch.Error
			default:
				return nil, nil
			}
			return []session.Event{{Kind: "task", Role: e.Subtype, Task: task}}, nil
		}
		if e.Subtype == "local_command_output" {
			return []session.Event{{Kind: "message", ID: e.UUID, Role: "Claude", Text: e.Content}}, nil
		}
		if e.Subtype == "init" {
			if a.resumeSession != "" && e.Parent == "" && e.SessionID != a.resumeSession {
				return nil, fmt.Errorf("native resumed session identity changed")
			}
			return []session.Event{{Kind: "session", SessionID: e.SessionID, Model: e.Model}}, nil
		}
	case "stream_event":
		if a.streams == nil {
			a.streams = make(map[string]string)
		}
		s := e.Event
		switch s.Type {
		case "message_start":
			a.streams[e.Parent] = s.Message.ID
		case "content_block_stop":
			return a.toolDelta(e.Parent, s.Index, "", true), nil
		case "content_block_start":
			if s.Block.Type == "text" && a.streams[e.Parent] != "" {
				id := a.streams[e.Parent]
				text, err := a.message(id).set(s.Index, s.Block.Text, false)
				if err != nil {
					return nil, err
				}
				if text != "" {
					return []session.Event{{Kind: "message", ID: id, Role: "Claude", Text: text}}, nil
				}
			}
			if s.Block.Type == "tool_use" {
				a.startTool(e.Parent, s.Index, s.Block.ID, s.Block.Name)
				return []session.Event{{Kind: "tool", ID: s.Block.ID, Role: s.Block.Name, Text: string(s.Block.Input)}}, nil
			}
		case "content_block_delta":
			if s.Delta.Type == "input_json_delta" {
				return a.toolDelta(e.Parent, s.Index, s.Delta.PartialJSON, false), nil
			}
			if s.Delta.Type != "text_delta" {
				break
			}
			id := a.streams[e.Parent]
			if id == "" {
				break
			}
			message := a.message(id)
			if s.Index < 0 || s.Index >= 4096 {
				return nil, fmt.Errorf("invalid Claude content block index")
			}
			previous := ""
			if s.Index < len(message.blocks) && message.blocks[s.Index] != nil {
				previous = message.blocks[s.Index].text
			}
			text, err := message.set(s.Index, previous+s.Delta.Text, false)
			if err != nil {
				return nil, err
			}
			return []session.Event{{Kind: "message", ID: id, Role: "Claude", Text: text}}, nil
		}
	case "assistant":
		var blocks []content
		if err := json.Unmarshal(e.Message.Content, &blocks); err != nil {
			return nil, err
		}
		var result []session.Event
		for _, block := range blocks {
			switch block.Type {
			case "text":
				message := a.message(e.Message.ID)
				index := len(message.blocks)
				for i, old := range message.blocks {
					if old != nil && !old.final {
						index = i
						break
					}
				}
				text, err := message.set(index, block.Text, true)
				if err != nil {
					return nil, err
				}
				// One SDK assistant event completes one native content block, not
				// necessarily the whole same-ID message. Keep prior blocks intact.
				if len(result) > 0 && result[len(result)-1].Kind == "message" {
					result[len(result)-1].Text = text
				} else {
					result = append(result, session.Event{Kind: "message", ID: e.Message.ID, Role: "Claude", Text: text})
				}
			case "tool_use":
				result = append(result, session.Event{Kind: "tool", ID: block.ID, Role: block.Name, Text: string(block.Input)})
				if command := decodeCommand(block.Name, string(block.Input), true); command != nil {
					result = append(result, session.Event{Kind: "command_preview", ID: block.ID, Role: block.Name, Caller: e.Parent, CommandInput: command})
				}
				if edit := decodeEdit(block.Name, string(block.Input), true); edit != nil {
					result = append(result, session.Event{Kind: "edit", ID: block.ID, Role: block.Name, Edit: edit, Caller: e.Parent})
				}
			}
		}
		return result, nil
	case "user":
		if len(e.Message.Content) == 0 || e.Message.Content.Kind() == '"' {
			return nil, nil
		}
		var blocks []content
		if err := json.Unmarshal(e.Message.Content, &blocks); err != nil {
			return nil, err
		}
		var result []session.Event
		var response struct {
			BackgroundTaskID string `json:"backgroundTaskId"`
		}
		if e.ToolResult.Kind() == '{' {
			_ = json.Unmarshal(e.ToolResult, &response)
		}
		for _, block := range blocks {
			if block.Type == "tool_result" {
				event := session.Event{Kind: "tool_result", ID: block.ToolUseID, Text: contentText(block.Content), Failed: block.IsError}
				if response.BackgroundTaskID != "" && len(blocks) == 1 {
					event.Output = &session.CommandOutput{TaskID: response.BackgroundTaskID}
				}
				result = append(result, event)
			}
		}
		return result, nil
	case "result":
		clear(a.tools)
		clear(a.text)
		clear(a.streams)
		result := []session.Event{{Kind: "done", ID: e.UUID, Failed: e.IsError, Text: strings.Join(e.Errors, "\n"), SessionID: e.SessionID}}
		if e.StartupFailure == "" && (!e.IsError || len(e.Usage.Models) > 0) && (len(e.Usage.Models) > 0 || e.Usage.CostUSD != nil) {
			result = append(result, session.Event{Kind: "usage", Usage: &e.Usage})
		}
		return result, nil
	}
	return nil, nil
}
func contentText(value jsontext.Value) string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	var blocks []content
	if json.Unmarshal(value, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// Text blocks retain native stream indices until the turn ends. Completed SDK
// blocks reconcile the first unsettled text block in that same message.
type textBlock struct {
	text  string
	final bool
}
type textMessage struct {
	blocks []*textBlock
	bytes  int
}

func (a *adapter) message(id string) *textMessage {
	if a.text == nil {
		a.text = make(map[string]*textMessage)
	}
	if a.text[id] == nil {
		a.text[id] = new(textMessage)
	}
	return a.text[id]
}
func (m *textMessage) set(index int, text string, final bool) (string, error) {
	if index < 0 || index >= 4096 {
		return "", fmt.Errorf("invalid Claude content block index")
	}
	for len(m.blocks) <= index {
		m.blocks = append(m.blocks, nil)
	}
	previous := 0
	if m.blocks[index] != nil {
		previous = len(m.blocks[index].text)
	}
	size := m.bytes - previous + len(text)
	if size > frameLimit {
		return "", fmt.Errorf("Claude streamed message exceeds the 8 MiB display limit")
	}
	m.blocks[index] = &textBlock{text: text, final: final}
	m.bytes = size
	var result strings.Builder
	result.Grow(size)
	for _, block := range m.blocks {
		if block != nil {
			result.WriteString(block.text)
		}
	}
	return result.String(), nil
}
