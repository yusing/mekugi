package observation

// CoordinationProjection is frozen by native with its response envelope. It is
// presentation evidence, not another work ledger or proof of model consumption.
type CoordinationProjection struct {
	Deliveries []CoordinationTerminalDelivery `json:"deliveries"`
	Wait       *CoordinationWaitSettlement    `json:"wait"`
	Interrupt  *CoordinationInterruptRequest  `json:"interrupt,omitzero"`
}
type CoordinationInterruptRequest struct {
	OperationID string             `json:"operationId"`
	Target      CoordinationTarget `json:"target"`
}
type CoordinationTarget struct {
	ThreadID string `json:"threadId"`
	TaskName string `json:"taskName"`
	WorkID   string `json:"workId"`
}
type CoordinationTerminalDelivery struct {
	DeliveryID         string             `json:"deliveryId"`
	Source             CoordinationTarget `json:"source"`
	Disposition        string             `json:"disposition"`
	Recipient          string             `json:"recipient"`
	Body               string             `json:"body"`
	CaptureResourceRef *string            `json:"captureResourceRef"`
}
type CoordinationWaitSettlement struct {
	WaitID         string                    `json:"waitId"`
	CallerThreadID string                    `json:"callerThreadId"`
	CallerWorkID   string                    `json:"callerWorkId"`
	Origin         CoordinationWaitOrigin    `json:"origin"`
	Mode           string                    `json:"mode"`
	Selected       []CoordinationTarget      `json:"selected"`
	ClaimID        *string                   `json:"claimId"`
	Result         CoordinationWaitResult    `json:"result"`
	Messages       []CoordinationWaitMessage `json:"messages"`
	Outcomes       []CoordinationWaitOutcome `json:"outcomes"`
}
type CoordinationWaitOrigin struct {
	Kind     string `json:"kind"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId,omitzero"`
	WindowID string `json:"windowId,omitzero"`
	CellID   string `json:"cellId,omitzero"`
	Reason   string `json:"reason,omitzero"`
}
type CoordinationWaitResult struct {
	Kind  string                        `json:"kind"`
	Wake  string                        `json:"wake,omitzero"`
	Cause *CoordinationWaitCancellation `json:"cause,omitzero"`
}
type CoordinationWaitCancellation struct {
	Kind       string  `json:"kind"`
	TurnID     string  `json:"turnId,omitzero"`
	DeliveryID string  `json:"deliveryId,omitzero"`
	Diagnostic *string `json:"diagnostic,omitzero"`
}
type CoordinationWaitMessage struct {
	DeliveryID     string  `json:"deliveryId"`
	SenderThreadID *string `json:"senderThreadId"`
	SenderTaskName *string `json:"senderTaskName"`
	Recipient      string  `json:"recipient"`
	Body           string  `json:"body"`
}

// Wait outcomes include paused reserved work. Paused is not a terminal delivery.
type CoordinationWaitOutcome struct {
	Target      CoordinationTarget `json:"target"`
	Disposition string             `json:"disposition"`
}
type AgentCoordination struct {
	ThreadID   string                 `json:"threadId"`
	TurnID     string                 `json:"turnId"`
	Projection CoordinationProjection `json:"projection"`
}

// ContextResetEvent is the native reset receipt, not a request or execution handle.
type ContextResetEvent struct {
	Thread     string `json:"threadId"`
	Turn       string `json:"turnId"`
	RequestID  string `json:"requestId,omitempty"`
	Generation uint64 `json:"generation"`
	ResetID    string `json:"resetId,omitempty"`
	Status     string `json:"status"`
	Inline     bool   `json:"inline"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

// NativeMessage is a retained native response carrier. Its provenance and
// attribution are validated by the presentation consumer before delivery.
type NativeMessage struct {
	Type      string              `json:"type"`
	ID        string              `json:"id"`
	Role      string              `json:"role"`
	Author    string              `json:"author"`
	Recipient string              `json:"recipient"`
	Content   []NativeMessageText `json:"content"`
	Metadata  struct {
		Kinds []string `json:"content_item_kinds"`
	} `json:"internal_chat_message_metadata_passthrough"`
}
type NativeMessageText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
