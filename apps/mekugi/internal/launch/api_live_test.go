// Source: apps/mekugi/internal/launch/web_live_test.go:1:67@[14a6d1f3]
package launch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
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
	"github.com/yusing/mekugi/modules/threadcontrol/control"
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
	command := "python3 -c " + quoteShellWord(`print("first")`) + "; false"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "/usr/bin/bash -lc " + quoteShellWord(command), Status: "failed", ExitCode: new(1), AggregatedOutput: new("first\n")}})
	file := diffmodel.RenderReviewFile("/project/source.go", "/project/source.go", "old\n", "new\n")
	saved := (changepresentation.Publication{}).With(changepresentation.GroupCaptures([]changepresentation.Chunk{{Key: "change1", Workspace: workspace, Review: file, Change: "change1", Caller: "/root", Source: "apply_patch"}}))
	u.reviewSource.PublishReview(u.attached.session.Scope(), &changepresentation.Snapshot{Publication: saved}, workspace, "complete", "")
	live := newLivePreview(t.Context())
	defer testReview(live).Close()
	setLiveScope(live, preview.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
	live.Update(preview.Preview{ID: "edit", Thread: "main", Workspace: workspace, Caller: "/root", Status: "streaming", Files: []diffmodel.ReviewFile{file}}, false)
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, _ sessionapi.Start, api *sessionapi.Server) error {
		publisher := apiLive{server: api, coordination: control.New(), started: time.Now()}
		publisher.publish(&u.attached, &u.Client, testReview(live), u.reviewSource, false, false, time.Now())
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
				Values []struct {
					Code      string `json:"code"`
					Label     string `json:"label"`
					BatchExit bool   `json:"batchExit"`
					ExitCode  int    `json:"exitCode"`
				} `json:"values"`
			} `json:"entries"`
		} `json:"main"`
	}
	if err := json.Unmarshal(event.Data, &state); err != nil {
		t.Fatal(err)
	}
	savedFile := readAPIReviewFile(t, endpoint, rpc, state.Review.Page)
	if event.Kind != "snapshot" || len(savedFile.Chunks) != 1 || savedFile.Chunks[0].Review.Diff != file.Diff || len(state.Live) != 1 || state.Live[0].ID != "edit" || state.Live[0].Files[0].Diff != file.Diff {
		t.Fatalf("missing review or preview: %s", event.Data)
	}
	commandShown, resultShown := false, false
	for _, entry := range state.Main.Entries {
		for _, value := range entry.Values {
			commandShown = commandShown || value.Code == `print("first")`
			resultShown = resultShown || value.BatchExit && value.Code == "" && value.Label == "" && value.ExitCode == 1
		}
	}
	if !commandShown || !resultShown {
		t.Fatalf("compound command lost its steps or combined result: %s", event.Data)
	}
	for _, entry := range state.Main.Entries {
		if len(entry.Images) == 1 {
			body := readAPIResource(t, endpoint, "image", "main", map[string]any{"seq": entry.Seq, "agent": entry.Agent, "index": 0, "image": entry.Images[0]})
			if !bytes.Equal(body, data) {
				t.Fatal("viewed image bytes changed")
			}

			return
		}
	}
	t.Fatalf("missing viewed-image binding: %s", event.Data)
}

func TestAPICommandFeedRetainsOnlyCurrentCommands(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	script := "python3 -c " + quoteShellWord(`print("first")`) + "; false"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": binding.ThreadItem{ID: "seed", Type: "commandExecution", Command: "/usr/bin/bash -lc " + quoteShellWord(script), Status: "failed", ExitCode: new(1), AggregatedOutput: new("first\n")}})
	source := u.attached.source
	seed := source.Entries[0].Source
	feed := commandFeed{}
	publish := func() *activity.Publication {
		return feed.publication(source, source.Publication(u.attached.presentation, nil))
	}
	first := publish()
	command := first.Entries[0].Command
	if command == nil || len(feed.content) != 1 {
		t.Fatal("compound command was not enriched")
	}
	if publish() != first {
		t.Fatal("unchanged publication was copied")
	}
	// A pending command survives trimming, even with newer sequence holes.
	source.Model.Mutate(0, func(record *sessionpresentation.ActivityRecord) {
		native := *record.Source.Native
		native.Running = true
		record.Source.Native = &native
	})
	pending := publish().Entries[0].Command
	for i := range activity.FeedLimit {
		entry := seed
		entry.Seq += uint64(i + 1)
		entry.CallID = fmt.Sprintf("command-%d", entry.Seq)
		native := *seed.Native
		native.Item = entry.CallID
		native.Invocation = nil
		native.Exit = new(1)
		native.Duration = time.Second
		native.CommandEnded = time.Now()
		entry.Native = &native
		source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{entry}})
	}
	current := publish()
	if len(current.Entries) != activity.FeedLimit || len(feed.content) != len(current.Entries) || current.Entries[0].Command != pending {
		t.Fatalf("eviction lost a pending command or retained obsolete commands: feed=%d cache=%d", len(current.Entries), len(feed.content))
	}
	removed := current.Entries[1].Seq
	unchanged := current.Entries[2].Command
	source.Model.Remove(1, 2)
	withHole := publish()
	if _, exists := feed.content[removed]; exists || len(feed.content) != len(withHole.Entries) || withHole.Entries[1].Command != unchanged {
		t.Fatal("hole removal retained a command or replaced unchanged content")
	}
	// Membership also ends when a row no longer needs command enrichment.
	source.Model.Mutate(0, func(record *sessionpresentation.ActivityRecord) {
		record.Values = []activity.Value{{Kind: "op", Code: "printf first; false"}}
	})
	withoutBatch := publish()
	if _, exists := feed.content[seed.Seq]; exists || withoutBatch.Entries[0].Command != nil {
		t.Fatal("ineligible row retained command content")
	}
	if withoutBatch.Entries[1].Command != unchanged {
		t.Fatal("unrelated row update replaced unchanged command content")
	}
	source.Model.Mutate(1, func(record *sessionpresentation.ActivityRecord) {
		native := *record.Source.Native
		native.Command = "printf updated; false"
		record.Source.Native = &native
	})
	updated := publish()
	if got := updated.Entries[1].Command; got == unchanged || got == nil || got.Details == nil || got.Details.CommandText != "printf updated; false" {
		t.Fatalf("revised command was not refreshed: %+v", got)
	}
	if publish() != updated {
		t.Fatal("unchanged revised publication was copied")
	}
	if first.Entries[0].Command != command {
		t.Fatal("previous publication was mutated")
	}
}

func TestAPILiveFamilyReviewAndWaitingRoster(t *testing.T) {
	u, threads, _ := logicalWaitUI(t, new(int64(1000)))
	workspace := u.attached.cwd
	capture := func(key, caller, before, after string, order uint64) changepresentation.Publication {
		return changepresentation.Publication{}.With(changepresentation.GroupCaptures([]changepresentation.Chunk{{
			Key: key, Workspace: workspace, CaptureOrder: order, Caller: caller, Change: key,
			Review: diffmodel.RenderReviewFile(filepath.Join(workspace, "file.go"), filepath.Join(workspace, "file.go"), before, after),
		}}))
	}
	root := capture("root1", "/root", "one\n", "one\nparent\n", 1)
	u.reviewSource.PublishReview(u.attached.session.Scope(), &changepresentation.Snapshot{Publication: root}, workspace, "complete", "")
	child := changepresentation.ReviewView{Revision: 1, Workspace: workspace, Publication: capture("child1", "/root/worker", "one\nparent\n", "one\nchild\n", 2)}
	refresh := make(chan chan struct{})
	launcher := &nativeReattachLauncher{run: func(ctx context.Context, _ sessionapi.Start, api *sessionapi.Server) error {
		publisher := apiLive{server: api, coordination: threads.shared, started: time.Now()}
		now := time.Now()
		publisher.publish(&u.attached, &u.Client, nil, u.reviewSource, false, false, now)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case done := <-refresh:
				now = now.Add(apiFrameInterval)
				publisher.publish(&u.attached, &u.Client, nil, u.reviewSource, false, false, now)
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
				Content *sessionapi.ContentReference `json:"content"`
				Name    string                       `json:"name"`
				State   string                       `json:"state"`
				Wait    *struct {
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
	savedFile := readAPIReviewFile(t, endpoint, rpc, state.Review.Page)
	if len(savedFile.Origins) != 2 || len(state.Review.Callers) != 2 {
		t.Fatalf("shared publication lost family captures: %s", event.Data)
	}
	for _, caller := range state.Review.Callers {
		cursor := state.Review.Page
		cursor.Caller = caller.Caller
		file := readAPIReviewFile(t, endpoint, rpc, cursor)
		if len(file.Origins) != 1 || file.Origins[0].Caller != caller.Caller {
			t.Fatalf("caller projection borrowed another caller's edits: %+v", caller)
		}
	}
	for _, agent := range state.Roster.Agents {
		if agent.Name == "/root" {
			if agent.Content != nil {
				var content struct {
					Agent jsontext.Value `json:"agent"`
				}
				if err := json.Unmarshal(readAPIResource(t, endpoint, "content", "main", agent.Content), &content); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(content.Agent, &agent); err != nil {
					t.Fatal(err)
				}
				t.Logf("hydrated %d-byte deferred roster row", len(content.Agent))
			}
			if agent.State != "waiting" || agent.Wait == nil || agent.Wait.Body != "Waiting for" || agent.Wait.WaitTimeout != "1s" || len(agent.Wait.Waits) != 1 || agent.Wait.Waits[0].Name != "/root/worker" {
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

func readAPIReviewFile(t *testing.T, endpoint *httptest.Server, rpc *sessionapi.RPCClient, cursor sessionapi.PageCursor) apiReviewFile {
	t.Helper()
	var page sessionapi.PageResult
	if err := json.Unmarshal(serverRPCCall(t, rpc, "session/page", "main", cursor), &page); err != nil || len(page.Files) != 1 {
		t.Fatalf("expected one saved review file: %v", err)
	}
	var content struct {
		Review apiReviewFile `json:"review"`
	}
	if err := json.Unmarshal(readAPIResource(t, endpoint, "content", "main", page.Files[0].Content), &content); err != nil {
		t.Fatal(err)
	}
	return content.Review
}

func apiTestIntent(t *testing.T, u *appServerUI, intent sessionapi.Intent) string {
	t.Helper()
	refusal, err := u.nativeSession.applyAPIIntent(&u.attached, &u.picker, &u.Editor, u.clock, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.ctx, intent)
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

	view := publishTestPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.settings, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side)
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
	refusal, err := u.nativeSession.submitAPIShell(&u.attached, &u.picker, u.clock, u.ctx, "false")
	if err != nil || refusal != "" {
		t.Fatalf("shell: %q, %v", refusal, err)
	}
	if !strings.Contains(wire.String(), `"method":"thread/shellCommand"`) || u.Text != "local prose" {
		t.Fatalf("shell dispatch changed draft: %q, %s", u.Text, wire)
	}
	shellResponse(&u.attached, &u.Client, &u.Editor, u.clock, &u.shellEdits, &appserver.Error{Message: "rejected"})
	if u.Text != "local prose" {
		t.Fatalf("remote rejection replaced local draft: %q", u.Text)
	}
	history := u.attached.conversation.Editor.InputHistory
	if len(history) != 1 || history[0].Text != "!false" {
		t.Fatalf("rejected command history: %+v", history)
	}
}

func readAPIResource(t *testing.T, endpoint *httptest.Server, kind, thread string, body any) []byte {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(sessionapi.RPCParams{Thread: thread, Body: encoded})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint.URL+sessionapi.ResourcePath+kind, bytes.NewReader(params))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(sessionapi.ProtocolHeader, sessionapi.ProtocolVersion)
	response, err := endpoint.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("resource status %d: %s, %v", response.StatusCode, data, err)
	}
	return data
}
