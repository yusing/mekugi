package launch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestSideForkUsesOwningNativeRuntime(t *testing.T) {
	for _, command := range []string{"/btw question", "/side", "/oneoff task"} {
		t.Run(command, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			workspace := installAPIRuntimeHost(t)
			launcher := &nativeReattachLauncher{run: func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
				_, err := runServerSession(ctx, []string{"--shell-guard=false"}, []string{"--yolo"}, api, start.Workspace, nil)
				return err
			}, prepare: prepareAPIInput}
			hub := sessionapi.NewHub(t.Context(), launcher)
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
			execution := &threadExecution{hub: hub}
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
				info, err := owner.Thread(t.Context(), "side")
				if err != nil || info.Thread != "side" {
					t.Fatalf("side request preparation cannot read its host thread: %+v, %v", info, err)
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
