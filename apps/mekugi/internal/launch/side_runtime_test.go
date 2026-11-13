package launch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeChildContextUsesSharedRuntimeWithoutView(t *testing.T) {
	u, execution, _ := nativeRunningUI(t)
	execution.hub = sessionapi.NewHub(t.Context(), nil)
	t.Cleanup(execution.hub.Close)
	child := execution.native.agent("child")
	child.Loaded, child.Metrics = true, appserver.AgentMetrics{ContextTokens: new(int64(123))}
	if execution.native.byThread["child"] != nil {
		t.Fatal("fixture unexpectedly owns a child view")
	}
	for _, test := range []struct {
		name, target string
		loaded       bool
	}{{"admitted", "child", true}, {"unloaded", "child", false}, {"foreign", "foreign", true}} {
		t.Run(test.name, func(t *testing.T) {
			child.Loaded = test.loaded
			done := make(chan error, 1)
			go func() {
				owner, err := execution.Runtime(test.target)
				if err == nil && owner != u.Requests {
					err = fmt.Errorf("child acquired another runtime")
				}
				if err == nil {
					_, err = owner.Inspect(t.Context(), test.target)
				}
				done <- err
			}()
			u.observeInspection(<-u.Requests.Inspections())
			if err := <-done; (err == nil) != (test.target == "child" && test.loaded) {
				t.Fatalf("native ownership validation for %s: %v", test.target, err)
			}
		})
	}
	cache := u.conversationState.Attachments(u.attached.session.Thread("child"))
	cache.Set("reset-window", []composer.ContextAttachment{{ID: "retained-attachment"}})
	done := make(chan error, 1)
	go func() {
		snapshot, err := u.Requests.ContextAttachments(t.Context(), "child", "reset-window")
		if err == nil && (len(snapshot.Values) != 1 || snapshot.Values[0].ID != "retained-attachment") {
			err = fmt.Errorf("child attachment context changed: %+v", snapshot)
		}
		done <- err
	}()
	u.observeInspection(<-u.Requests.Inspections())
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if execution.native.byThread["child"] != nil || u.Host.Input.(*appServerTestInput).Len() != 0 {
		t.Fatal("passive child context inspection created a view or native request")
	}
}

func TestSideForkUsesOwningNativeRuntime(t *testing.T) {
	for _, command := range []string{"/btw question", "/oneoff task"} {
		t.Run(command, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			workspace := installAPIRuntimeHost(t)
			execution := &threadExecution{}
			launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
				_, err := runServerSession(ctx, []string{"--shell-guard=false"}, []string{"--yolo"}, api, start.Workspace, execution)
				return err
			}, prepare: prepareAPIInput}
			hub := sessionapi.NewHub(t.Context(), launcher)
			execution.hub = hub
			defer hub.Close()
			endpoint := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
			defer endpoint.Close()
			client := &http.Client{Timeout: 10 * time.Second}
			rpc, err := sessionapi.OpenRPC(ctx, client, endpoint.URL+sessionapi.RPCPath, "")
			if err != nil {
				t.Fatal(err)
			}
			defer rpc.Close()
			serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Workspace: workspace, Model: "fixture"})
			native, err := sessionapi.OpenNative(ctx, client, endpoint.URL, "main")
			if err != nil {
				t.Fatal(err)
			}
			defer native.Close()
			event, err := native.Next()
			if err != nil {
				t.Fatal(err)
			}
			execution = &threadExecution{hub: hub}
			if _, err := execution.Runtime("side"); err == nil {
				t.Fatal("unadmitted side acquired a runtime")
			}
			_, err = native.Send(t.Context(), event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
				Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: "main", Operation: sessionpresentation.SubmitComposer},
				Draft:  composer.Draft{Text: command},
			}})
			if err != nil {
				t.Fatal(err)
			}
			for {
				event, err := native.Next()
				if err != nil {
					t.Fatal(err)
				}
				view := event.Frame.View
				if view.SideThread.ID != "side" && (view.BTW == nil || view.BTW.Status != "Answering") {
					continue
				}
				owner, err := execution.Runtime("side")
				if err != nil {
					t.Fatalf("admitted side has no running native runtime: %v", err)
				}
				main, err := execution.Runtime("main")
				if err != nil || owner != main {
					t.Fatalf("side did not borrow Main's request owner: %v", err)
				}
				info, err := owner.(*sessioncontrol.Requests[appServerRequest]).Thread(t.Context(), "side")
				if err != nil || info.ID != "side" {
					t.Fatalf("side request preparation cannot read its host thread: %+v, %v", info, err)
				}
				if _, err := owner.Inspect(ctx, "side"); err != nil {
					t.Fatalf("side request preparation cannot inspect its own context: %v", err)
				}
				break
			}
			native.Close()
			hub.Close()
			if _, err := execution.Runtime("side"); err == nil {
				t.Fatal("side retained execution after its runtime joined")
			}
		})
	}
}
