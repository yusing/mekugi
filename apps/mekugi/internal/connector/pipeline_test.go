package connector

import (
	"context"
	"encoding/json/v2"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func TestPipelinedObservationRetainsInitializationAdmission(t *testing.T) {
	for _, admission := range []string{"accepted", "token", "version"} {
		t.Run(admission, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			hub := sessionapi.NewHub(ctx, nil)
			defer hub.Close()
			socket, _ := upstream(t, sessionapi.Versioned(hub.Handler()))
			bridge, tokenPath := connect(t, socket)
			server := httptest.NewServer(bridge)
			defer server.Close()
			conn := dial(t, server.URL, nil)
			version, token := sessionapi.ProtocolVersion, readToken(t, tokenPath)
			if admission == "token" {
				token = "refused"
			}
			if admission == "version" {
				version = "incompatible"
			}
			params, err := json.Marshal(sessionapi.RPCInitialize{Version: version, Token: token})
			if err != nil {
				t.Fatal(err)
			}
			// Both messages leave before reading the initialization response.
			for _, body := range []string{`{"jsonrpc":"2.0","id":"init","method":"initialize","params":` + string(params) + `}`, `{"jsonrpc":"2.0","id":"observe","method":"session/subscribe","params":{"subscription":"startup"}}`} {
				if err := conn.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			read := func() sessionapi.RPCMessage {
				t.Helper()
				_, data, err := conn.Read(ctx)
				var message sessionapi.RPCMessage
				if err != nil || json.Unmarshal(data, &message) != nil {
					t.Fatalf("control reply: %v", err)
				}
				return message
			}
			first := read()
			if string(first.ID) != `"init"` {
				t.Fatal("observation preceded initialization")
			}
			if admission != "accepted" {
				status := 401
				if admission == "version" {
					status = 409
				}
				if first.Error == nil || first.Error.Data.Status != status {
					t.Fatal("incorrect initialization refusal")
				}
				if _, _, err := conn.Read(ctx); err == nil {
					t.Fatal("refused initialization admitted a queued observation")
				}
				return
			}
			var initialized sessionapi.RPCInitialize
			if first.Error != nil || json.Unmarshal(first.Result, &initialized) != nil || initialized.ServerTime == 0 {
				t.Fatal("initialization omitted confirmed clock")
			}
			if next := read(); string(next.ID) != `"observe"` || next.Error != nil {
				t.Fatal("queued observation was refused")
			}
			if next := read(); next.Method != "subscription/event" {
				t.Fatal("queued observation lost its first publication")
			}
		})
	}
}
