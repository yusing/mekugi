package launch

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func hookTrustTestStartup(t *testing.T, status string) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, wire := newAppServerTestUI()
	u.Thread, u.attached.cwd, u.reviewHooks = "", "/workspace", true
	appServerTestRequest(t, u, `"initialize"`, "initialize")
	appServerTestMessage(t, u, `{"id":"initialize","result":{"userAgent":"codex/0.162.0"}}`)
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 2 || requests[0].Method != "initialized" || requests[1].Method != "thread/start" {
		t.Fatalf("startup: %+v", requests)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"test","reasoningEffort":"high","thread":{"id":"main","cwd":"/workspace"}}}`, requests[1].ID))
	requests = appServerTurnRequests(t, wire)
	var inventory int
	for _, request := range requests {
		if request.Method == "hooks/list" {
			inventory = request.ID
		}
	}
	if inventory == 0 || !u.HookReview || u.AcceptsInput() {
		t.Fatalf("startup did not gate execution for hook inventory: %+v", requests)
	}
	hook := map[string]any{"key": "/workspace/.codex/hooks.json:session_start:0:0", "currentHash": "sha256:changed-content", "trustStatus": status, "sourcePath": "/workspace/.codex/hooks.json", "eventName": "sessionStart", "handlerType": "command", "command": "bash scripts/startup.sh", "enabled": true}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, inventory, guardHookTestResult(t, u.attached.cwd, hook)))
	return u, wire
}

func TestHookTrustStartupDecisions(t *testing.T) {
	for _, status := range []string{"untrusted", "modified"} {
		for _, approve := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/approve=%v", status, approve), func(t *testing.T) {
				u, wire := hookTrustTestStartup(t, status)
				if wire.Len() != 0 || !u.approvals.Open || len(u.approvals.calls()) != 1 {
					t.Fatalf("startup bypassed review: %s %+v", wire.String(), u.approvals)
				}
				if err := u.testPaint(io.Discard, 82, 31); err != nil {
					t.Fatal(err)
				}
				paint := ansi.Strip(strings.Join(terminalTest(u).Frame.Rows, "\n"))
				for _, want := range []string{"Trust this hook?", "bash scripts/startup.sh", "Trust: " + status, "Continue without trusting"} {
					if !strings.Contains(paint, want) {
						t.Fatalf("missing %q in rendered prompt:\n%s", want, paint)
					}
				}
				if !approve {
					appServerTestKeys(t, u, "\r")
					if wire.Len() != 0 || u.HookReview || !u.AcceptsInput() {
						t.Fatal("decline did not release execution without writing trust")
					}
					return
				}
				appServerTestKeys(t, u, "2\r")
				type request struct {
					ID     int            `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				writes := appServerDrainRequests[request](t, wire)
				if len(writes) != 1 || writes[0].Method != "config/batchWrite" || writes[0].Params["reloadUserConfig"] != true {
					t.Fatalf("trust persistence: %+v", writes)
				}
				edits, _ := json.Marshal(writes[0].Params["edits"])
				if !strings.Contains(string(edits), `"trusted_hash":"sha256:changed-content"`) || strings.Contains(string(edits), `"enabled"`) {
					t.Fatalf("wrong trust write: %s", edits)
				}
				appServerTestKeys(t, u, "\r")
				if wire.Len() != 0 || len(u.approvals.calls()) != 1 {
					t.Fatal("duplicate answer or premature startup")
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"configuration is read-only"}}`, writes[0].ID))
				if wire.Len() != 0 || len(u.approvals.calls()) != 1 || !strings.Contains(u.attached.feedback.Text, "read-only") {
					t.Fatal("failed persistence lost pending decision")
				}
				appServerTestKeys(t, u, "\r")
				retry := appServerOneRequest(t, wire, "config/batchWrite", "")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, retry.ID))
				if wire.Len() != 0 || u.HookReview || !u.AcceptsInput() {
					t.Fatal("saved trust did not release execution")
				}
				if len(u.approvals.calls()) != 0 {
					t.Fatal("saved approval remains pending")
				}
			})
		}
	}
}

func TestHookTrustAlreadyTrusted(t *testing.T) {
	for _, status := range []string{"trusted", "managed"} {
		u, wire := hookTrustTestStartup(t, status)
		if wire.Len() != 0 || u.HookReview {
			t.Fatal("trusted hook blocked execution")
		}
		if len(u.approvals.calls()) != 0 {
			t.Fatal("trusted hook prompted again")
		}
	}
}

func TestHookTrustBeforeResume(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Thread, u.attached.cwd, u.reviewHooks = "", "/invocation", true
	resumeTestMembers(t, u, "saved")
	u.Startup.Thread = "saved"
	appServerTestRequest(t, u, `"initialize"`, "initialize")
	appServerTestMessage(t, u, `{"id":"initialize","result":{}}`)
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 2 || requests[0].Method != "initialized" || requests[1].Method != "thread/read" {
		t.Fatalf("resume preflight: %+v", requests)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"saved","cwd":"/destination"}}}`, requests[1].ID))
	resume := appServerOneRequest(t, wire, "thread/resume", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"test","reasoningEffort":"high","thread":{"id":"saved","cwd":"/destination","turns":[]}}}`, resume.ID))
	type hookRequest struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			Thread string `json:"threadId"`
		} `json:"params"`
	}
	hooks := appServerDrainRequests[hookRequest](t, wire)
	var inventory int
	for _, request := range hooks {
		if request.Method == "hooks/list" && request.Params.Thread == "saved" {
			inventory = request.ID
		}
	}
	if inventory == 0 {
		t.Fatalf("reviewed invocation instead of resumed thread: %+v", hooks)
	}
	result := guardHookTestResult(t, "/destination", map[string]any{"key": "saved-hook", "currentHash": "sha256:changed", "trustStatus": "modified", "command": "saved-command", "enabled": true})
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, inventory, result))
	if wire.Len() != 0 || len(u.approvals.calls()) != 1 {
		t.Fatal("resume bypassed hook review")
	}
	if err := u.testPaint(io.Discard, 82, 31); err != nil {
		t.Fatal(err)
	}
	appServerTestKeys(t, u, "\r")
	if wire.Len() != 0 || u.HookReview {
		t.Fatal("decline did not release resumed thread")
	}
}

func TestHookTrustStartupSharedApprovalConsumer(t *testing.T) {
	u, wire := hookTrustTestStartup(t, "modified")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.activity, u.Requests)
	defer n.close()
	request := sessionapi.NativeRequest{Attach: true, Attachment: "startup", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}
	if refusal := n.attach(request, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["startup"]
	view := publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, true)
	approvals := nativePendingApprovals(u.attached.conversation)
	if len(approvals) != 1 || approvals[0].Title != "Trust this hook?" || view.Main.ID != "main" {
		t.Fatalf("startup shared projection: %+v %+v", view, approvals)
	}
	a, choice, err := v.attached.presentation.Approval(sessionpresentation.ApprovalIntent{Version: view.Version, Request: approvals[0].ID, Choice: 0}, u.approvals.Approvals)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.applyApproval(&u.attached, &u.picker, &u.Editor, u.clock, v.attached.grants, u.approvals.handles[a], choice, ""); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || u.HookReview {
		t.Fatal("shared approval did not release execution")
	}
}

func TestUISnapshotHookTrustStartup(t *testing.T) {
	u, _ := hookTrustTestStartup(t, "modified")
	rows, _ := u.testMainFrame(80, 22, 0)
	assertNativeUISnapshot(t, "native-hook-trust-startup", rows)
}

func TestHookTrustBrowserStartupCanAttachBeforeConsent(t *testing.T) {
	u, _ := hookTrustTestStartup(t, "modified")
	u.attached.cwd = t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, _ sessionapi.Start, api *sessionapi.Server) error {
		publisher := apiLive{server: api, started: time.Now()}
		publisher.publish(&u.attached, &u.Client, nil, nil, false, false, time.Now())
		<-ctx.Done()
		return ctx.Err()
	}}
	hub := sessionapi.NewHub(ctx, launcher)
	defer hub.Close()
	endpoint := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer endpoint.Close()
	rpc, err := sessionapi.OpenRPC(ctx, endpoint.Client(), endpoint.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	body, err := json.Marshal(sessionapi.Start{Workspace: u.attached.cwd, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := rpc.Call(ctx, "session/start", sessionapi.RPCParams{Body: body}, &result); err != nil {
		t.Fatal("browser creation could not attach to pending hook consent:", err)
	}
	if result.ID != "main" || !u.HookReview || u.AcceptsInput() || apiInputRefusal(&u.attached, &u.Client, false) == "" {
		t.Fatal("attachment readiness bypassed execution consent")
	}
}
