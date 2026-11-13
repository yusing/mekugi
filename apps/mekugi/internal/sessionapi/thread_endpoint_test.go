package sessionapi

import (
	"context"
	"errors"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestNativeThreadEndpointsShareManagedOwnerAndFamily(t *testing.T) {
	launcher := newManagedLauncher()
	hub := NewHub(t.Context(), launcher)
	t.Cleanup(hub.Close)
	workspace := t.TempDir()
	if _, err := hub.startSession(t.Context(), testJSON(t, Start{Workspace: workspace, Model: "codex", Resume: "root"})); err != nil {
		t.Fatal(err)
	}
	root := <-launcher.started
	child, err := hub.AdoptThread(root, "child", "root", workspace, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := hub.AdoptThread(root, "sibling", "root", workspace, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	members := []sessionpresentation.Participant{{Thread: "root", Task: "/root"}, {Thread: "child", Parent: "root", Task: "/root/child"}, {Thread: "sibling", Parent: "root", Task: "/root/sibling"}}
	for id, server := range map[string]*Server{"root": root, "child": child, "sibling": sibling} {
		if err := server.SetSummary(Summary{Thread: id, Workspace: workspace, Ready: true}); err != nil {
			t.Fatal(err)
		}
		frame := Frame{Session: Session{Thread: id, Workspace: workspace}, FamilyRoot: "root", Members: members}
		if id == "child" {
			frame.Questions = []prompt.CallView{{ID: "original-host-request"}}
		}
		if err := server.Publish(frame); err != nil {
			t.Fatal(err)
		}
	}
	hub.mu.Lock()
	owned := len(hub.running) == 1 && hub.sessions["root"].runtime == hub.sessions["child"].runtime && hub.sessions["child"].server != root
	hub.mu.Unlock()
	if !owned || launcher.count.Load() != 1 {
		t.Fatal("endpoint adoption created a process owner or evicted root")
	}
	if hub.questionOwner(sibling, "original-host-request") != child {
		t.Fatal("family answer lost original child owner")
	}
	family, _ := hub.Family("child")
	if family == nil || family.Root != "root" || family.Agents == nil || len(family.Agents.Roster) != 3 {
		t.Fatalf("shared-process family omitted endpoints: %+v", family)
	}
	if _, err := hub.AdoptThread(root, "child", "root", workspace, func(context.Context) error { return nil }); err == nil {
		t.Fatal("duplicate endpoint replaced live authority")
	}
	hub.RetireThread(child, "child", nil)
	if !root.ActiveThread("root") || !root.ActiveThread("sibling") || root.ActiveThread("child") {
		t.Fatal("view endpoint retirement canceled shared execution")
	}
}

func TestNativeChildEndWaitsForJoinAndDoesNotCancelInvocation(t *testing.T) {
	launcher := newManagedLauncher()
	hub := NewHub(t.Context(), launcher)
	t.Cleanup(hub.Close)
	workspace := t.TempDir()
	if _, err := hub.startSession(t.Context(), testJSON(t, Start{Workspace: workspace, Model: "codex", Resume: "root"})); err != nil {
		t.Fatal(err)
	}
	root := <-launcher.started
	called, joined := make(chan struct{}), make(chan struct{})
	_, err := hub.AdoptThread(root, "child", "root", workspace, func(ctx context.Context) error {
		close(called)
		select {
		case <-joined:
			return errors.New("native refusal")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	connection := &rpcConnection{endpoint: &rpcEndpoint{hub: hub}}
	result := make(chan *RPCError, 1)
	go func() {
		_, failure, _ := connection.dispatch(t.Context(), "session/end", RPCParams{Thread: "child"})
		result <- failure
	}()
	<-called
	select {
	case <-result:
		t.Fatal("end acknowledged before native join")
	default:
	}
	if !root.ActiveThread("root") {
		t.Fatal("child end canceled invocation")
	}
	close(joined)
	if failure := <-result; failure == nil {
		t.Fatal("native refusal reported success")
	}
	if !root.ActiveThread("child") || !root.ActiveThread("root") {
		t.Fatal("rejected end discarded admitted endpoint")
	}
}
