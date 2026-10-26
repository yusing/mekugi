// Source: apps/mekugi/internal/launch/web_live_test.go:1:67@[14a6d1f3]
package launch

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	activity "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffmodel"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestAPILiveReviewPreviewAndViewedImage(t *testing.T) {
	workspace := t.TempDir()
	u := newAppServerSessionTestUI(t, workspace)
	const raster = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="
	data, err := base64.StdEncoding.DecodeString(raster)
	if err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(workspace, "image.png")
	if err := os.WriteFile(imagePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{ID: "image", Type: "imageView", Path: imagePath}})
	file := diffmodel.RenderReviewFile("/project/source.go", "/project/source.go", "old\n", "new\n")
	saved := (changepresentation.Publication{}).With(changepresentation.GroupCaptures([]changepresentation.Chunk{{Key: "change1", Workspace: workspace, Review: file, Origin: changepresentation.Origin{Change: "change1", Caller: "/root", Source: "apply_patch"}}}))
	u.reviewSource.PublishReview(u.attached.session.Scope(), &changepresentation.Snapshot{Publication: saved}, workspace, "complete", "")
	live := newLivePreview(t.Context())
	defer testReview(live).Close()
	setLiveScope(live, preview.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	live.Update(preview.Preview{ID: "edit", Thread: "main", Workspace: workspace, Caller: "/root", Status: "streaming", Files: []diffmodel.ReviewFile{file}}, false)
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, _ sessionapi.Start, api *sessionapi.Server) error {
		publisher := apiLive{server: api, started: time.Now()}
		publisher.publish(&u.attached, u.shell, &u.Client, testReview(live), u.reviewSource, false, false, time.Now())
		<-ctx.Done()
		return ctx.Err()
	}}
	hub := sessionapi.NewHub(t.Context(), launcher)
	defer hub.Close()
	endpoint := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer endpoint.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), endpoint.Client(), endpoint.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture"})
	events := serverRPCSubscribe(t, rpc, "main")
	defer events.Close()
	wait, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	event, err := events.Next(wait)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Review struct {
			Page sessionapi.PageCursor `json:"page"`
		} `json:"review"`
		Live []preview.Preview `json:"live"`
		Main struct {
			Entries []struct {
				Seq    uint64   `json:"seq"`
				Agent  string   `json:"agent"`
				Images []string `json:"images"`
			} `json:"entries"`
		} `json:"main"`
	}
	if err := json.Unmarshal(event.Data, &state); err != nil {
		t.Fatal(err)
	}
	savedFile := readAPIReviewFile(t, rpc, state.Review.Page)
	if event.Kind != "snapshot" || len(savedFile.Chunks) != 1 || savedFile.Chunks[0].Review.Diff != file.Diff || len(state.Live) != 1 || state.Live[0].ID != "edit" || state.Live[0].Files[0].Diff != file.Diff {
		t.Fatalf("missing review or preview: %s", event.Data)
	}
	for _, entry := range state.Main.Entries {
		if len(entry.Images) == 1 {
			body := serverRPCCall(t, rpc, "session/image", "main", map[string]any{"seq": entry.Seq, "agent": entry.Agent, "index": 0, "image": entry.Images[0]})
			var image struct {
				MIME string `json:"mime"`
				Data string `json:"data"`
			}
			if err := json.Unmarshal(body, &image); err != nil || image.MIME != "image/png" || image.Data != raster {
				t.Fatalf("viewed image response: %s, %v", body, err)
			}
			return
		}
	}
	t.Fatalf("missing viewed-image binding: %s", event.Data)
}

func TestAPILiveFamilyReviewAndWaitingRoster(t *testing.T) {
	u, threads, _ := logicalWaitUI(t, new(int64(1000)))
	workspace := u.attached.cwd
	capture := func(key, caller, before, after string, order uint64) changepresentation.Publication {
		return changepresentation.Publication{}.With(changepresentation.GroupCaptures([]changepresentation.Chunk{{
			Key: key, Workspace: workspace, CaptureOrder: order, Origin: changepresentation.Origin{Caller: caller, Change: key},
			Review: diffmodel.RenderReviewFile(filepath.Join(workspace, "file.go"), filepath.Join(workspace, "file.go"), before, after),
		}}))
	}
	root := capture("root1", "/root", "one\n", "one\nparent\n", 1)
	u.reviewSource.PublishReview(u.attached.session.Scope(), &changepresentation.Snapshot{Publication: root}, workspace, "complete", "")
	child := changepresentation.ReviewView{Revision: 1, Workspace: workspace, Publication: capture("child1", "/root/worker", "one\nparent\n", "one\nchild\n", 2)}
	refresh := make(chan chan struct{})
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, _ sessionapi.Start, api *sessionapi.Server) error {
		publisher := apiLive{server: api, threads: threads, started: time.Now()}
		now := time.Now()
		publisher.publish(&u.attached, u.shell, &u.Client, nil, u.reviewSource, false, false, now)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case done := <-refresh:
				now = now.Add(apiFrameInterval)
				publisher.publish(&u.attached, u.shell, &u.Client, nil, u.reviewSource, false, false, now)
				close(done)
			}
		}
	}}
	hub := sessionapi.NewHub(t.Context(), launcher)
	threads.hub = hub
	defer hub.Close()
	endpoint := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer endpoint.Close()
	rpc, err := sessionapi.OpenRPC(t.Context(), endpoint.Client(), endpoint.URL+sessionapi.RPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture"})
	childReady := make(chan struct{})
	_, err = hub.StartPrepared(t.Context(), "main", "", workspace, func(ctx context.Context, api *sessionapi.Server) error {
		if err := api.SetSummary(sessionapi.Summary{Thread: "child", Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		if err := api.Publish(sessionapi.Frame{FamilyRoot: "main", Session: sessionapi.Session{Thread: "child", Workspace: workspace}, Review: &child, Agents: &activity.Publication{}}); err != nil {
			return err
		}
		close(childReady)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-childReady
	done := make(chan struct{})
	refresh <- done
	<-done
	events := serverRPCSubscribe(t, rpc, "main")
	defer events.Close()
	wait, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	event, err := events.Next(wait)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Review struct {
			Page    sessionapi.PageCursor `json:"page"`
			Callers []struct {
				Caller string `json:"caller"`
			} `json:"callers"`
		} `json:"review"`
		Roster struct {
			Agents []struct {
				Name  string `json:"name"`
				State string `json:"state"`
				Wait  *struct {
					Body        string `json:"body"`
					WaitTimeout string `json:"waitTimeout"`
					Waits       []struct {
						Name string `json:"name"`
					} `json:"waits"`
				} `json:"wait"`
			} `json:"agents"`
		} `json:"roster"`
	}
	if err := json.Unmarshal(event.Data, &state); err != nil {
		t.Fatal(err)
	}
	savedFile := readAPIReviewFile(t, rpc, state.Review.Page)
	if len(savedFile.Origins) != 2 || len(state.Review.Callers) != 2 {
		t.Fatalf("shared publication lost family captures: %s", event.Data)
	}
	for _, caller := range state.Review.Callers {
		cursor := state.Review.Page
		cursor.Caller = caller.Caller
		file := readAPIReviewFile(t, rpc, cursor)
		if len(file.Origins) != 1 || file.Origins[0].Caller != caller.Caller {
			t.Fatalf("caller projection borrowed another caller's edits: %+v", caller)
		}
	}
	for _, agent := range state.Roster.Agents {
		if agent.Name == "/root" {
			if agent.State != "waiting" || agent.Wait == nil || agent.Wait.Body != "Waiting for" || agent.Wait.WaitTimeout != "1s" || len(agent.Wait.Waits) != 1 || agent.Wait.Waits[0].Name != "all" {
				t.Fatalf("shared roster lost canonical logical wait: %s", event.Data)
			}
			return
		}
	}
	t.Fatal("shared roster lost Main")
}

type apiReviewFile struct {
	Chunks  []changepresentation.Chunk  `json:"chunks"`
	Origins []changepresentation.Origin `json:"origins"`
}

func readAPIReviewFile(t *testing.T, rpc *sessionapi.RPCClient, cursor sessionapi.PageCursor) apiReviewFile {
	t.Helper()
	var page sessionapi.PageResult
	if err := json.Unmarshal(serverRPCCall(t, rpc, "session/page", "main", cursor), &page); err != nil || len(page.Files) != 1 {
		t.Fatalf("expected one saved review file: %v", err)
	}
	var content struct {
		Review apiReviewFile `json:"review"`
	}
	if err := json.Unmarshal(serverRPCCall(t, rpc, "session/content", "main", page.Files[0].Content), &content); err != nil {
		t.Fatal(err)
	}
	return content.Review
}

func apiTestIntent(t *testing.T, u *appServerUI, intent sessionapi.Intent) string {
	t.Helper()
	refusal, err := applyAPIIntent(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, u.notifications, intent)
	if err != nil {
		t.Fatal(err)
	}
	return refusal
}

// Browser input steers Main's turn and Stop interrupts it, as Enter and Ctrl-C
// do, while the terminal displays a side conversation; the session status
// still describes Main.
func TestAPIIntentsActOnMain(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "main-turn"
	oneoffTestStart(t, u, wire)

	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Text: " /status"}); refusal == "" || wire.Len() != 0 {
		t.Fatalf("command input refusal = %q, wire = %s", refusal, wire)
	}
	u.attached.conversation.Approvals.Open = true
	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Text: "go on"}); refusal != "Answer the open prompt first." || wire.Len() != 0 {
		t.Fatalf("input under an open prompt refusal = %q, wire = %s", refusal, wire)
	}
	u.attached.conversation.Approvals.Open = false

	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Text: "also check the docs"}); refusal != "" {
		t.Fatalf("input refused: %q", refusal)
	}
	steer := appServerOneRequest(t, wire, "turn/steer", "also check the docs")
	if steer.Params.ThreadID != "main" || steer.Params.ExpectedTurnID != "main-turn" {
		t.Fatalf("steer = %+v", steer.Params)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"main-turn"}}`, steer.ID))

	view := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side)
	if !u.attached.presentation.DisplayingSide() || !view.Status.Session.Running || !sessionpresentation.MainInterruptible(&u.Live, u.attached.conversation) {
		t.Fatalf("Main's status while the side conversation shows = %+v", view.Status.Session)
	}
	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Stop}); refusal != "" {
		t.Fatalf("stop refused: %q", refusal)
	}
	if r := appServerOneRequest(t, wire, "turn/interrupt", ""); r.Params.ThreadID != "main" || u.Interruption.Target != "main-turn" {
		t.Fatalf("interrupt = %+v, target %q", r.Params, u.Interruption.Target)
	}
}

func TestAPIIntentsRejectStaleAndCanceledAdmission(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, intent := range []sessionapi.Intent{
		{Action: sessionapi.Send, Text: "stale", Thread: "previous", Workspace: "/workspace"},
		{Action: sessionapi.Stop, Thread: "main", Workspace: "/previous"},
		{Action: sessionapi.Send, Text: "canceled", Context: ctx},
	} {
		if refusal := apiTestIntent(t, u, intent); refusal == "" || wire.Len() != 0 {
			t.Fatalf("inadmissible request reached host: refusal=%q wire=%s", refusal, wire)
		}
	}
}

// Source: apps/mekugi/internal/launch/web_shell_test.go:124:138@[14a6d1f3]
func TestAPIShellRejectionPreservesLocalDraft(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Text = "local prose"
	refusal, err := submitAPIShell(&u.attached, u.shell, &u.Client, &u.picker, u.clock, u.restoring != nil, u.skillEnvironment, u.ctx, u.review, &u.shellEdits, u.retained, u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, "false")
	if err != nil || refusal != "" {
		t.Fatalf("shell: %q, %v", refusal, err)
	}
	if !strings.Contains(wire.String(), `"method":"thread/shellCommand"`) || u.Text != "local prose" {
		t.Fatalf("shell dispatch changed draft: %q, %s", u.Text, wire)
	}
	shellResponse(&u.attached, u.shell, &u.Client, &u.Editor, u.clock, &u.shellEdits, &appserver.Error{Message: "rejected"})
	if u.Text != "local prose" {
		t.Fatalf("remote rejection replaced local draft: %q", u.Text)
	}
	history := u.attached.conversation.Editor.InputHistory
	if len(history) != 1 || history[0].Text != "!false" {
		t.Fatalf("rejected command history: %+v", history)
	}
}
