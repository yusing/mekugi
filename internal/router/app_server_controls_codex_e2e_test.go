//go:build journal_e2e

package router

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

func TestNativeControlsChildPaneNativeCodexE2E(t *testing.T) {
	provider := &appServerChildMetadataProvider{turns: make(map[string]int)}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server", "-c",
		`model_providers.controls={name="controls",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
		"-c", `model_provider="controls"`, "-c", `model="gpt-6-astra"`,
		"-c", `features.multi_agent_v2={enabled=true,tool_namespace="collaboration"}`,
		"-c", "tools.update_plan.enabled=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env, cmd.Dir = routerFaultCodexEnvironment(t), t.TempDir()
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := &appServerUI{client: client, view: newLiveActivityView(), agents: newLiveActivityView(), requests: make(map[string]string), ctx: ctx}
	u.ensureShell()
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	if err := u.request("initialize", nil); err != nil {
		t.Fatal(err)
	}
	started, sawActivity := false, false
	for {
		select {
		case m, ok := <-client.Messages:
			if !ok {
				t.Fatal("host closed before child lifecycle")
			}
			if err := u.message(m); err != nil {
				t.Fatal(err)
			}
			if u.thread != "" && !started {
				started = true
				appServerTestKeys(t, u, "Spawn the assigned child.\r")
			}
			if u.shell.autoActivity && !u.shell.journalOpen {
				sawActivity = true
			}
			if sawActivity && !u.shell.autoActivity && u.shell.journalOpen {
				return
			}
		case <-ctx.Done():
			t.Fatalf("actual child did not show Activity then Journal: %v", ctx.Err())
		}
	}
}
