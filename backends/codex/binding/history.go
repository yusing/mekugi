package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// ThreadItem retains native item payloads without presentation state.
// Source: apps/mekugi/internal/router/app_server_ui.go:70:107@[eaad597b] appServerItem.
type ThreadItem struct {
	DurationMS       *int64           `json:"durationMs"`
	Delivery         string           `json:"delivery"`
	Questions        []Question       `json:"questions"`
	ID               string           `json:"id"`
	ClientID         string           `json:"clientId"` // userMessage: the submission's clientUserMessageId.
	Type             string           `json:"type"`
	Text             string           `json:"text"`
	Summary          []string         `json:"summary"`
	Command          string           `json:"command"`
	Cwd              string           `json:"cwd"` // commandExecution: the directory the host ran it in.
	Source           string           `json:"source"`
	ProcessID        string           `json:"processId"`
	Path             string           `json:"path"`
	SavedPath        string           `json:"savedPath"`
	Status           string           `json:"status"`
	AggregatedOutput *string          `json:"aggregatedOutput"`
	Query            string           `json:"query"`
	Action           *WebSearchAction `json:"action"`
	Results          []jsontext.Value `json:"results"`
	// Content is variant-specific: user input blocks or raw reasoning strings.
	// Decode only userMessage content; reasoning presentation uses Summary.
	Content        jsontext.Value  `json:"content"`
	Phase          string          `json:"phase"`
	ExitCode       *int            `json:"exitCode"`
	CommandActions []CommandAction `json:"commandActions"`
	Changes        []FileChange    `json:"changes"`
	Tool           string          `json:"tool"`
	Server         string          `json:"server"`
	Arguments      jsontext.Value  `json:"arguments"`
	Result         jsontext.Value  `json:"result"`
	Error          jsontext.Value  `json:"error"`
}

// Source: apps/mekugi/internal/router/app_server_events.go:74:82@[eaad597b] appServerFileChange.
type FileChange struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
	Kind struct {
		Type      string `json:"type"`
		MovePath  string `json:"move_path"`
		MovePath2 string `json:"movePath"`
	} `json:"kind"`
}

// Source: apps/mekugi/internal/router/app_server_events.go:84:89@[eaad597b] appServerCommandAction.
type CommandAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Path    string `json:"path"`
	Query   string `json:"query"`
}

// Source: apps/mekugi/internal/router/app_server_search.go:12:18@[eaad597b] appServerWebSearchAction.
type WebSearchAction struct {
	Type    string   `json:"type"`
	Query   string   `json:"query"`
	Queries []string `json:"queries"`
	URL     string   `json:"url"`
	Pattern string   `json:"pattern"`
}

// Source: apps/mekugi/internal/router/app_server_events.go:91:104@[eaad597b] appServerThreadInfo.
type ThreadInfo struct {
	Name           string        `json:"name"`
	ID             string        `json:"id"`
	ParentThreadID string        `json:"parentThreadId"`
	AgentNickname  string        `json:"agentNickname"`
	AgentRole      string        `json:"agentRole"`
	Cwd            string        `json:"cwd"`
	Path           string        `json:"path"`
	CreatedAt      int64         `json:"createdAt"`
	UpdatedAt      int64         `json:"updatedAt"`
	Turns          []HistoryTurn `json:"turns"`
}

// Source: apps/mekugi/internal/router/app_server_resume.go:203:213@[eaad597b] appServerHistoryTurn.
type HistoryTurn struct {
	ID          string       `json:"id"`
	Status      string       `json:"status"`
	Items       []ThreadItem `json:"items"`
	StartedAt   int64        `json:"startedAt"`
	CompletedAt int64        `json:"completedAt"`
	Error       *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// DecodeHistoryThread admits a thread/read or thread/resume response only for
// the requested thread identity. Decoding does not restore live operations.
// Source: apps/mekugi/internal/router/app_server_resume.go:49:58@[eaad597b] resumeSettingsResponse.
func DecodeHistoryThread(raw []byte, requested string) (ThreadInfo, error) {
	var result struct {
		Thread ThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ThreadInfo{}, err
	}
	if result.Thread.ID == "" {
		return ThreadInfo{}, errors.New("thread/read returned no thread identity")
	}
	if result.Thread.ID != requested {
		return ThreadInfo{}, errors.New("thread/read returned a different thread identity")
	}
	return result.Thread, nil
}
