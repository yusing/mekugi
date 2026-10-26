package sessionapi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeAdmissionPreparedReservationSharedAcrossStarts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	workspace := t.TempDir()
	nativeResult := make(chan error, 2)
	launcher := &nativeStartupLauncher{}
	launcher.managedLauncher = *newManagedLauncher()
	launcher.run = func(ctx context.Context, start Start, server *Server) error {
		if start.Native == nil {
			return launcher.managedLauncher.Run(ctx, start, server)
		}
		err := server.ReserveThread("retained")
		nativeResult <- err
		return err
	}
	hub := NewHub(ctx, launcher)
	defer hub.Close()
	if _, failure := hub.startSession(ctx, testJSON(t, Start{Workspace: workspace, Model: "codex", Resume: "parent"})); failure != nil {
		t.Fatal(failure)
	}
	<-launcher.started

	started := make(chan context.Context, 1)
	publish := make(chan struct{})
	confirmed := make(chan error, 1)
	request, disconnect := context.WithCancel(ctx)
	defer disconnect()
	returned := make(chan error, 1)
	go func() {
		_, err := hub.StartPrepared(request, "parent", "retained", workspace, func(runtime context.Context, server *Server) error {
			started <- runtime
			select {
			case <-publish:
			case <-runtime.Done():
				return runtime.Err()
			}
			if err := server.ReserveThread("retained"); err != nil {
				confirmed <- err
				return err
			}
			err := server.SetSummary(Summary{Thread: "retained", Workspace: workspace, Ready: true})
			confirmed <- err
			if err != nil {
				return err
			}
			<-runtime.Done()
			return runtime.Err()
		})
		returned <- err
	}()
	var runtime context.Context
	select {
	case runtime = <-started:
	case <-ctx.Done():
		t.Fatal("prepared execution did not start")
	}
	disconnect()
	if err := <-returned; err == nil || errors.Is(err, ErrPreparedNotStarted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("admitted request cancellation lost its uncertain outcome: %v", err)
	}
	if runtime.Err() != nil {
		t.Fatalf("request cancellation stopped admitted execution: %v", runtime.Err())
	}
	hub.mu.Lock()
	var owner *managedSession
	for current := range hub.running {
		if current.resume == "retained" {
			owner = current
		}
	}
	hub.mu.Unlock()
	if owner == nil {
		t.Fatal("admitted prepared owner lost its pending reservation")
	}

	for _, phase := range []string{"pending", "ready"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "ready" {
				close(publish)
				if err := <-confirmed; err != nil {
					t.Fatal(err)
				}
				select {
				case id := <-owner.ready:
					if id != "retained" || !hub.activeThread(id) {
						t.Fatalf("prepared readiness lost admitted identity: %q", id)
					}
				case <-ctx.Done():
					t.Fatal("prepared owner did not reach readiness after requester disconnected")
				}
			}
			_, failure := hub.startSession(ctx, testJSON(t, Start{Workspace: workspace, Model: "codex", Resume: "retained"}))
			if failure == nil || failure.Data.Status != 409 || launcher.count.Load() != 1 {
				t.Fatalf("ordinary start bypassed prepared ownership: failure=%v, launches=%d", failure, launcher.count.Load())
			}
			subscription, failure := hub.startNative(ctx, ctx, testJSON(t, NativeStart{Workspace: workspace}))
			if failure != nil {
				t.Fatal(failure)
			}
			defer subscription.close()
			select {
			case err := <-nativeResult:
				if !errors.Is(err, errThreadOwned) {
					t.Fatalf("native start bypassed prepared ownership: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("native execution did not attempt identity reservation")
			}
		})
	}
}
