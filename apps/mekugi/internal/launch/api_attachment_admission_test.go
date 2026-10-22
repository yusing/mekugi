package launch

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/responses"
)

func TestAPIAttachmentFinalIdentityTextLimit(t *testing.T) {
	workspace := t.TempDir()
	input := sessionapi.InputRequest{Attachments: []sessionapi.Attachment{{Source: "upload", Name: "x.txt", Data: new(base64.StdEncoding.EncodeToString([]byte("x")))}}}
	size, err := prepareAPIInput(t.Context(), workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	defer size.Cleanup()
	// Fill the old pre-identity calculation exactly. Final host framing must
	// refuse this before image ownership or queued-input admission transfers.
	n := responses.UserTextLimit - len(size.Draft.Text) - len(size.Draft.Attachments[0]) - 1
	input.Text = strings.Repeat("a", n)
	prepared, err := prepareAPIInput(t.Context(), workspace, input)
	if prepared != nil {
		defer prepared.Cleanup()
	}
	if failure, ok := errors.AsType[*sessionapi.InputError](err); !ok || !failure.Limit {
		t.Fatalf("final-identity overflow was not a permanent preparation limit: %v", err)
	}
	launcher := &nativeReattachLauncher{
		prepare: prepareAPIInput,
		run: func(ctx context.Context, _ sessionapi.Start, api *sessionapi.Server) error {
			if err := api.SetSummary(sessionapi.Summary{Thread: "main", Workspace: workspace, Ready: true}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer endpoint.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), endpoint.Client(), endpoint.URL+"/api/control", "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture"})
	err = rpc.Call(t.Context(), "session/input", sessionapi.RPCParams{Thread: "main", Body: body}, nil)
	if failure, ok := errors.AsType[*sessionapi.RPCError](err); !ok || failure.Data == nil || failure.Data.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("permanent final-identity limit returned %v", err)
	}
	// Nearby valid content still reaches the existing host submission path.
	input.Text = input.Text[:len(input.Text)-256]
	prepared, err = prepareAPIInput(t.Context(), workspace, input)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()
	u, wire := newAppServerTestUI()
	u.startSession("main", workspace)
	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Text: input.Text, Prepared: prepared, Thread: "main", Workspace: workspace, Context: t.Context()}); refusal != "" {
		t.Fatal(refusal)
	}
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "turn/start" || len(requests[0].text()) > responses.UserTextLimit {
		t.Fatalf("valid near-limit input did not reach the host within budget: requests=%d", len(requests))
	}
}
