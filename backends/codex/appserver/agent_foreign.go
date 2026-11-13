package appserver

// AgentForeignOrigin is established by the source native owner. Transport request
// IDs provide correlation only; this tuple identifies the original operation.
type AgentForeignOrigin struct {
	RootThreadID string `json:"rootThreadId"`
	ActorID      string `json:"actorId"`
	AgentPath    string `json:"agentPath"`
	TurnID       string `json:"turnId"`
	CallID       string `json:"callId"`
}

// AgentForeignOperation is the native tagged union. Only message, sideQuestion,
// list and inspect exist; the destination native decoder validates each variant.
type AgentForeignOperation struct {
	Type       string  `json:"type"`
	Message    *string `json:"message,omitzero"`
	Question   *string `json:"question,omitzero"`
	PathPrefix *string `json:"pathPrefix,omitzero"`
}

type AgentForeignRequest struct {
	Source         AgentForeignOrigin    `json:"source"`
	TargetThreadID string                `json:"targetThreadId"`
	Operation      AgentForeignOperation `json:"operation"`
}

// AgentForeignResponse borrows the native observation contract. The Hub forwards
// it unchanged and never supplies acceptance, settings or work state itself.
type AgentForeignResponse struct {
	Type           string                `json:"type"`
	TargetThreadID string                `json:"targetThreadId,omitzero"`
	DeliveryID     string                `json:"deliveryId,omitzero"`
	Answer         *string               `json:"answer,omitzero"`
	Accepted       *bool                 `json:"accepted,omitzero"`
	Error          *string               `json:"error,omitzero"`
	Tree           *AgentTreeObservation `json:"tree,omitzero"`
	Agent          *AgentObservation     `json:"agent,omitzero"`
}
