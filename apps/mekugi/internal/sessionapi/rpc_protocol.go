package sessionapi

import (
	"context"
	"encoding/json/jsontext"
	"io"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/goutils/synk"
)

// Control RPC is independent of the Codex host protocol. Both local and remote
// clients use these envelopes; the admitted endpoint establishes authority.
const (
	RPCPath         = "/api/control"
	NativeRPCPath   = "/api/native/control"
	RPCSubprotocol  = "mekugi.control.v" + ProtocolVersion
	RPCMessageLimit = InputBodyLimit + (64 << 10)
	// RPCPublicationLimit bounds one server-to-client message, including its
	// envelope. Relays must admit it so a large publication reaches its client.
	RPCPublicationLimit = 64 << 20
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
	Version    string `json:"version"`
	Token      string `json:"token,omitempty"`
	ServerTime int64  `json:"serverTime,omitzero"`
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
	Delivery     string         `json:"delivery,omitzero"`
	Subscription string         `json:"subscription"`
	Kind         string         `json:"kind"`
	Data         jsontext.Value `json:"data"`
}

type RPCClosed struct {
	Subscription string    `json:"subscription"`
	Error        *RPCError `json:"error,omitempty"`
}

// WriteMessage copies one message to conn and fails only after stall passes
// without the peer accepting more of it. A slow link that keeps receiving a
// large publication is not a stalled one; a whole-message deadline would retire it.
func WriteMessage(ctx context.Context, conn *websocket.Conn, typ websocket.MessageType, source io.Reader, stall time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(stall, cancel)
	defer timer.Stop()
	writer, err := conn.Writer(ctx, typ)
	if err != nil {
		return err
	}
	pool := synk.GetSizedBytesPool()
	buffer := pool.GetSized(32 << 10)
	defer pool.Put(buffer)
	for {
		n, err := source.Read(buffer)
		if n > 0 {
			if _, err := writer.Write(buffer[:n]); err != nil {
				return err
			}
			timer.Reset(stall)
		}
		if err == io.EOF {
			return writer.Close()
		}
		if err != nil {
			return err
		}
	}
}
