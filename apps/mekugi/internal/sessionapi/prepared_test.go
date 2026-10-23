package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

func preparedParent(t *testing.T) (*Hub, *managedLauncher, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	launcher := newManagedLauncher()
	hub := NewHub(ctx, launcher)
	t.Cleanup(func() { cancel(); hub.Close() })
	workspace := t.TempDir()
	_, failure := hub.startSession(ctx, testJSON(t, Start{Workspace: workspace, Model: "codex", Resume: "parent"}))
	if failure != nil {
		t.Fatal(failure)
	}
	<-launcher.started
	return hub, launcher, workspace
}

type preparedRequestOwner struct {
	threadcontrol.Runtime
	ctx   context.Context
	input threadcontrol.NativeInput
}

func (o *preparedRequestOwner) Input(ctx context.Context, input threadcontrol.NativeInput) (string, error) {
	o.ctx, o.input = ctx, input
	return "accepted-turn", nil
}

func TestHubPreparedIdentityAndRequestOwner(t *testing.T) {
	t.Parallel()
	hub, launcher, workspace := preparedParent(t)
	owner := new(preparedRequestOwner)
	parentOwner := new(preparedRequestOwner)
	hub.mu.Lock()
	parentServer := hub.sessions["parent"].server
	hub.mu.Unlock()
	parentServer.BindRequests(parentOwner)
	request, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	var calls atomic.Int32
	var runtimeContext context.Context
	id, err := hub.StartPrepared(request, "parent", "", workspace, func(ctx context.Context, server *Server) error {
		calls.Add(1)
		runtimeContext = ctx
		server.BindRequests(owner)
		if err := server.SetSummary(Summary{Thread: "confirmed-child", Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		// Readiness must not depend on an attached renderer or consuming input.
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil || id != "confirmed-child" {
		t.Fatalf("prepared identity: %q, %v", id, err)
	}
	conn := rawControl(t, hub.Handler(), false)
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"version":"2"}}`)
	if message := controlRead(t, conn); message.Error != nil {
		t.Fatal(message.Error)
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":2,"method":"session/list","params":{}}`)
	var list struct {
		Sessions []listed `json:"sessions"`
	}
	if message := controlRead(t, conn); message.Error != nil {
		t.Fatal(message.Error)
	} else if err := json.Unmarshal(message.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Thread != "parent" {
		t.Fatalf("top-level list includes child: %+v", list)
	}
	controlWrite(t, conn, `{"jsonrpc":"2.0","id":3,"method":"session/read","params":{"thread":"confirmed-child"}}`)
	var child listed
	if message := controlRead(t, conn); message.Error != nil {
		t.Fatal(message.Error)
	} else if err := json.Unmarshal(message.Result, &child); err != nil {
		t.Fatal(err)
	}
	if child.Thread != id || child.ParentThread != "parent" {
		t.Fatalf("child identity lost: %+v", child)
	}
	disconnect()
	if runtimeContext.Err() != nil || calls.Load() != 1 || launcher.count.Load() != 1 {
		t.Fatalf("disconnect changed admitted execution: runtime=%v, child runs=%d, launcher runs=%d", runtimeContext.Err(), calls.Load(), launcher.count.Load())
	}
	requests, err := hub.Requests(id)
	if err != nil || requests != owner {
		t.Fatalf("child request owner: %v, %v", requests, err)
	}
	parentRequests, err := hub.Requests("parent")
	if err != nil || parentRequests != parentOwner {
		t.Fatalf("parent request owner: %v, %v", parentRequests, err)
	}
	input := threadcontrol.NativeInput{Thread: id, ID: "stable-message", Message: "prepared", Run: true}
	result, err := requests.Input(t.Context(), input)
	if err != nil || result != "accepted-turn" || owner.ctx != t.Context() || owner.input != input || parentOwner.ctx != nil {
		t.Fatalf("request was not routed intact to the child owner: %+v, %s, %v", owner, result, err)
	}
}

func TestHubPreparedParentCancellationJoinsChild(t *testing.T) {
	t.Parallel()
	hub, _, workspace := preparedParent(t)
	cleaning := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	id, err := hub.StartPrepared(t.Context(), "parent", "", workspace, func(ctx context.Context, server *Server) error {
		server.BindRequests(new(preparedRequestOwner))
		if err := server.SetSummary(Summary{Thread: "child", Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		<-ctx.Done()
		close(cleaning)
		<-release
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	parent := hub.sessions["parent"].runtime
	child := hub.sessions[id].runtime
	hub.mu.Unlock()
	parent.cancel()
	select {
	case <-cleaning:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not observe parent cancellation")
	}
	select {
	case <-parent.done:
		t.Fatal("parent completed before child cleanup")
	default:
	}
	if _, err := hub.Requests(id); err == nil {
		t.Fatal("canceled child remained an available request owner")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-parent.done:
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not join child cleanup")
	}
	select {
	case <-child.done:
	default:
		t.Fatal("parent completed before the child runtime")
	}
	if hub.activeThread(id) {
		t.Fatal("joined child remained active")
	}
}

func TestPreparedChildParentIdentitySurvivesRuntimeSwitch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub, _, workspace := preparedParent(t)
		var child *Server
		_, err := hub.StartPrepared(t.Context(), "parent", "", workspace, func(ctx context.Context, server *Server) error {
			child = server
			if err := server.SetSummary(Summary{Thread: "child", Workspace: workspace, Ready: true}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		hub.mu.Lock()
		parent := hub.sessions["parent"].server
		hub.mu.Unlock()
		if err := parent.SetSummary(Summary{Thread: "switched", Workspace: workspace, Ready: true}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !hub.activeThread("switched") {
			t.Fatal("parent runtime did not switch threads")
		}
		if err := child.SetSummary(Summary{Thread: "child", Title: "updated", Workspace: workspace, Ready: true}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		hub.mu.Lock()
		entry := hub.sessions["child"].entry
		hub.mu.Unlock()
		if entry.Title != "updated" || entry.ParentThread != "parent" {
			t.Fatalf("child identity changed with parent runtime: %+v", entry)
		}
	})
}

func TestHubPreparedRejectsUnavailableParent(t *testing.T) {
	t.Parallel()
	hub, _, workspace := preparedParent(t)
	hub.mu.Lock()
	hub.sessions["replay"] = &listedSession{server: New()}
	hub.mu.Unlock()
	if _, err := hub.Requests("parent"); err == nil {
		t.Fatal("runtime without a bound ledger exposed a request owner")
	}
	hub.mu.Lock()
	parentRuntime := hub.sessions["parent"].runtime
	hub.mu.Unlock()
	parentRuntime.cancel()
	for _, parent := range []string{"missing", "replay", "parent"} {
		t.Run(parent, func(t *testing.T) {
			var ran atomic.Bool
			id, err := hub.StartPrepared(t.Context(), parent, "", workspace, func(context.Context, *Server) error {
				ran.Store(true)
				return nil
			})
			if id != "" || err == nil || ran.Load() {
				t.Fatalf("unavailable parent admitted: %q, %v, ran=%v", id, err, ran.Load())
			}
			if _, err := hub.Requests(parent); err == nil {
				t.Fatal("unavailable parent exposed a request owner")
			}
		})
	}
}

func TestHubPreparedResumeReservesRetainedIdentity(t *testing.T) {
	t.Parallel()
	hub, _, workspace := preparedParent(t)
	started, publish := make(chan struct{}), make(chan struct{})
	owner := new(preparedRequestOwner)
	done := make(chan error, 1)
	go func() {
		id, err := hub.StartPrepared(t.Context(), "parent", "retained", workspace, func(ctx context.Context, server *Server) error {
			close(started)
			select {
			case <-publish:
			case <-ctx.Done():
				return ctx.Err()
			}
			server.BindRequests(owner)
			if err := server.ReserveThread("retained"); err != nil {
				return err
			}
			if err := server.SetSummary(Summary{Thread: "retained", Workspace: workspace, Ready: true}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if err == nil && id != "retained" {
			err = errors.New("resume changed identity")
		}
		done <- err
	}()
	<-started
	for _, ready := range []bool{false, true} {
		if ready {
			close(publish)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		_, err := hub.StartPrepared(t.Context(), "parent", "retained", workspace, func(context.Context, *Server) error {
			return errors.New("duplicate runtime executed")
		})
		if !errors.Is(err, errThreadOwned) {
			t.Fatalf("ready=%t: duplicate resume: %v", ready, err)
		}
	}
	if runtime, err := hub.Requests("retained"); err != nil || runtime != owner || owner.input.Thread != "" {
		t.Fatalf("resume owner or unsolicited input: %v, %v, %+v", runtime, err, owner.input)
	}
}
