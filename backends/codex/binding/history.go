package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/contracts/observation"
)

// ThreadItem retains native item payloads without presentation state.
// Source: apps/mekugi/internal/router/app_server_ui.go:70:107@[eaad597b] appServerItem.
type ThreadItem struct {
	SourceAcquisition *observation.SourceAcquisition `json:"sourceAcquisition,omitzero"`
	CompactionMethod  observation.CompactionMethod   `json:"compactionMethod,omitzero"`
	DurationMS        *int64                         `json:"durationMs"`
	Delivery          string                         `json:"delivery"`
	Questions         []Question                     `json:"questions"`
	ID                string                         `json:"id"`
	ClientID          string                         `json:"clientId"` // userMessage: the submission's clientUserMessageId.
	Type              string                         `json:"type"`
	Name              string                         `json:"name"`
	Namespace         string                         `json:"namespace"`
	Output            jsontext.Value                 `json:"output"`
	Text              string                         `json:"text"`
	Summary           []string                       `json:"summary"`
	Command           string                         `json:"command"`
	Cwd               string                         `json:"cwd"` // commandExecution: the directory the host ran it in.
	Source            string                         `json:"source"`
	ProcessID         string                         `json:"processId"`
	Path              string                         `json:"path"`
	SavedPath         string                         `json:"savedPath"`
	Status            string                         `json:"status"`
	AggregatedOutput  *string                        `json:"aggregatedOutput"`
	Query             string                         `json:"query"`
	Action            *WebSearchAction               `json:"action"`
	Results           []jsontext.Value               `json:"results"`
	// Content is variant-specific: user input blocks or raw reasoning strings.
	// Decode only userMessage content; reasoning presentation uses Summary.
	Content           jsontext.Value  `json:"content"`
	Phase             string          `json:"phase"`
	ExitCode          *int            `json:"exitCode"`
	CommandActions    []CommandAction `json:"commandActions"`
	Changes           []FileChange    `json:"changes"`
	Tool              string          `json:"tool"`
	ReceiverThreadIDs []string        `json:"receiverThreadIds"`
	TimeoutMS         *int64          `json:"timeoutMs"`
	Server            string          `json:"server"`
	Arguments         jsontext.Value  `json:"arguments"`
	Result            jsontext.Value  `json:"result"`
	Error             jsontext.Value  `json:"error"`
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
	Model                *string        `json:"model"`
	ReasoningEffort      *string        `json:"reasoningEffort"`
	ServiceTier          *string        `json:"serviceTier"`
	ContextTokens        *uint64        `json:"contextTokens"`
	ContextWindow        *uint64        `json:"contextWindow"`
	CompletedCompactions *uint64        `json:"completedCompactions"`
	AgentStatus          jsontext.Value `json:"agentStatus"`
	CanAcceptDirectInput *bool          `json:"canAcceptDirectInput"`
	Source               jsontext.Value `json:"source"`
	HistoryMode          string         `json:"historyMode"`
	SessionID            string         `json:"sessionId"`
	ThreadSource         string         `json:"threadSource"`
	Name                 string         `json:"name"`
	ID                   string         `json:"id"`
	ParentThreadID       string         `json:"parentThreadId"`
	AgentNickname        string         `json:"agentNickname"`
	AgentRole            string         `json:"agentRole"`
	Cwd                  string         `json:"cwd"`
	Path                 string         `json:"path"`
	CreatedAt            int64          `json:"createdAt"`
	UpdatedAt            int64          `json:"updatedAt"`
	Turns                []HistoryTurn  `json:"turns"`
}

// AgentState decodes the stock native AgentStatus union without retaining its payload.
func (t ThreadInfo) AgentState() string {
	var state string
	if t.AgentStatus.Kind() == '"' {
		_ = json.Unmarshal(t.AgentStatus, &state)
	} else if t.AgentStatus.Kind() == '{' {
		var variants map[string]jsontext.Value
		if json.Unmarshal(t.AgentStatus, &variants) == nil && len(variants) == 1 {
			for name := range variants {
				state = name
			}
		}
	}
	return state
}

func (t ThreadInfo) IsLoaded() bool {
	switch t.AgentState() {
	case "", "shutdown", "not_found":
		return false
	default:
		return true
	}
}

func (t ThreadInfo) Details() observation.SessionDetails {
	return observation.SessionDetails{Model: t.Model, ReasoningEffort: t.ReasoningEffort, ServiceTier: t.ServiceTier,
		SessionMetrics: observation.SessionMetrics{ContextTokens: t.ContextTokens, ContextWindow: t.ContextWindow, Compactions: t.CompletedCompactions}}
}

// AgentPath reads native spawn metadata rather than a presentation alias.
func (t ThreadInfo) AgentPath() string {
	if t.ParentThreadID == "" {
		return "/root"
	}
	var source struct {
		SubAgent struct {
			Spawn struct {
				Path string `json:"agent_path"`
			} `json:"thread_spawn"`
		} `json:"subAgent"`
	}
	if json.Unmarshal(t.Source, &source) != nil {
		return ""
	}
	return source.SubAgent.Spawn.Path
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
