package sessionapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestTenDelegateFamilyLeavesIndependentStartupAndControlResponsive(t *testing.T) {
	hub, launcher, workspace := preparedParent(t)
	started := make(chan string, preparationLimit)
	release := make(chan struct{})
	defer close(release)
	for i := range 10 {
		id := fmt.Sprintf("child-%d", i)
		_, err := hub.StartPrepared(t.Context(), "parent", "", workspace, func(ctx context.Context, server *Server) error {
			server.prepare = func(ctx context.Context, _ string, input InputRequest) (*PreparedInput, error) {
				if input.Text == "busy" {
					started <- id
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return NewPreparedInput(composer.Draft{Text: input.Text}, nil), nil
			}
			if err := server.Publish(Frame{Session: Session{Thread: id, Workspace: workspace}, FamilyRoot: "parent"}); err != nil {
				return err
			}
			if err := server.SetSummary(Summary{Thread: id, Workspace: workspace, Ready: true}); err != nil {
				return err
			}
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case input := <-server.Intents():
					input.Reply <- ""
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	client := openTestRPC(t, hub.Handler(), false)
	for i := range preparationLimit {
		go func() {
			var result any
			_ = client.Call(t.Context(), "session/input", RPCParams{Thread: fmt.Sprintf("child-%d", i), Body: testJSON(t, InputRequest{Text: "busy"})}, &result)
		}()
	}
	for range preparationLimit {
		<-started
	}
	if reply := client.call(t, "session/input", RPCParams{Thread: "child-9", Body: testJSON(t, InputRequest{Text: "overflow"})}); reply.Code != 503 {
		t.Fatalf("descendants multiplied family preparation capacity: %d", reply.Code)
	}
	before := time.Now()
	if reply := managedRequest(t, hub, "session/start", "", Start{Workspace: workspace, Model: "codex", Resume: "independent"}); reply.Code != 202 {
		t.Fatalf("independent startup: %d %s", reply.Code, reply.Body)
	}
	independent := <-launcher.started
	for _, method := range []string{"session/input", "session/interrupt"} {
		if reply := client.call(t, method, RPCParams{Thread: "independent", Body: testJSON(t, InputRequest{Text: "probe"})}); reply.Code != 202 {
			t.Fatalf("independent %s: %d %s", method, reply.Code, reply.Body)
		}
	}
	if err := independent.Publish(Frame{Session: Session{Thread: "independent", Workspace: workspace}, Agents: familyActivity("independent", "independent publication")}); err != nil {
		t.Fatal(err)
	}
	family, _ := hub.Family("independent")
	if family.Agents.MainSummary[0].Text != "independent publication" {
		t.Fatal("busy family concealed independent publication")
	}
	t.Logf("10-delegate family, %d held preparations: independent startup, prepared input, interrupt and publication completed in %s", preparationLimit, time.Since(before))
}
