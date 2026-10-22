package sessionapi

import "encoding/json/jsontext"

// Control RPC is independent of the Codex host protocol. Both local and remote
// clients use these envelopes; the admitted endpoint establishes authority.
const (
	RPCPath         = "/api/control"
	NativeRPCPath   = "/api/native/control"
	RPCSubprotocol  = "mekugi.control.v2"
	RPCMessageLimit = InputBodyLimit + (64 << 10)
)

type RPCMessage struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitzero"`
	Method  string         `json:"method,omitempty"`
	Params  jsontext.Value `json:"params,omitzero"`
	Result  jsontext.Value `json:"result,omitzero"`
	Error   *RPCError      `json:"error,omitempty"`
}

type RPCError struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    *RPCErrorData `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

type RPCErrorData struct {
	Status  int    `json:"status"`
	Outcome string `json:"outcome"`
}

// Token is consumed only by the remote connector's initialization exchange.
// It is never forwarded to the session server or included in diagnostics.
type RPCInitialize struct {
	Version string `json:"version"`
	Token   string `json:"token,omitempty"`
}

// Body uses the operation's existing input type. Subscription is selected by
// the client before dispatch so early publications can be correlated safely.
type RPCParams struct {
	Thread       string         `json:"thread,omitempty"`
	Startup      string         `json:"startup,omitempty"`
	Subscription string         `json:"subscription,omitempty"`
	Body         jsontext.Value `json:"body,omitempty"`
}

type RPCSubscribed struct {
	Subscription string `json:"subscription"`
}

type RPCEvent struct {
	Subscription string         `json:"subscription"`
	Kind         string         `json:"kind"`
	Data         jsontext.Value `json:"data"`
}

type RPCClosed struct {
	Subscription string    `json:"subscription"`
	Error        *RPCError `json:"error,omitempty"`
}
