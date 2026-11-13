package appserver

// AgentObservation contains passive native facts. Nil means unavailable, not a
// root default. Reading these values neither loads a runtime nor starts work.
type AgentObservation struct {
	ThreadID             string                `json:"threadId"`
	RootThreadID         string                `json:"rootThreadId"`
	ParentThreadID       *string               `json:"parentThreadId"`
	Path                 *string               `json:"path"`
	Nickname             *string               `json:"nickname"`
	Role                 *string               `json:"role"`
	Backend              *string               `json:"backend"`
	Attachable           *bool                 `json:"attachable"`
	CanAcceptDirectInput *bool                 `json:"canAcceptDirectInput"`
	Loaded               bool                  `json:"loaded"`
	Status               *string               `json:"status"`
	Cwd                  *string               `json:"cwd"`
	Model                *string               `json:"model"`
	ReasoningEffort      *string               `json:"reasoningEffort"`
	ServiceTier          *string               `json:"serviceTier"`
	Metrics              AgentMetrics          `json:"metrics"`
	Work                 *AgentWorkObservation `json:"work"`
	Wait                 *AgentWaitObservation `json:"wait"`
}

type AgentMetrics struct {
	ContextTokens        *int64  `json:"contextTokens"`
	ContextWindow        *int64  `json:"contextWindow"`
	CompletedCompactions *uint64 `json:"completedCompactions"`
}

type AgentWorkObservation struct {
	WorkID             string  `json:"workId"`
	Revision           int64   `json:"revision"`
	Disposition        string  `json:"disposition"`
	TerminalDeliveryID *string `json:"terminalDeliveryId"`
}

type AgentWaitObservation struct {
	WaitID         string            `json:"waitId"`
	Mode           string            `json:"mode"`
	Phase          string            `json:"phase"`
	Selected       []AgentWaitTarget `json:"selected"`
	Reason         *string           `json:"reason"`
	TimeoutMS      *int64            `json:"timeoutMs"`
	DeadlineUnixMS *int64            `json:"deadlineUnixMs"`
}

type AgentWaitTarget struct {
	ThreadID string `json:"threadId"`
	WorkID   string `json:"workId"`
}

type AgentTreeObservation struct {
	RootThreadID        string             `json:"rootThreadId"`
	ObservationRevision uint64             `json:"observationRevision"`
	Agents              []AgentObservation `json:"agents"`
}

type AgentTreeChanged struct {
	RootThreadID        string `json:"rootThreadId"`
	ObservationRevision uint64 `json:"observationRevision"`
}

// TurnCheckpointed stops a host turn without settling native logical work.
// The tagged inner value deliberately keeps the protocol's snake_case fields.
type TurnCheckpointed struct {
	ThreadID   string         `json:"threadId"`
	TurnID     string         `json:"turnId"`
	WorkID     string         `json:"workId"`
	Checkpoint TurnCheckpoint `json:"checkpoint"`
	Targets    []string       `json:"targets"`
}

type TurnCheckpoint struct {
	Kind       string `json:"kind"`
	DeliveryID string `json:"delivery_id"`
}
