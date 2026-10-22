package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type managedReplayLauncher struct {
	catalog, prepared, runs atomic.Int32
	started                 chan *Server
	intents                 chan Intent
	canceled, stopped       chan struct{}
	ready, release          <-chan struct{}
}

func newManagedReplayLauncher() *managedReplayLauncher {
	return &managedReplayLauncher{started: make(chan *Server, 1), intents: make(chan Intent, 1), canceled: make(chan struct{}), stopped: make(chan struct{})}
}

func (l *managedReplayLauncher) StartOptions(context.Context) (StartOptions, error) {
	l.catalog.Add(1)
	return StartOptions{Models: []ModelGroup{{Models: []Model{{ID: "codex"}}}}}, nil
}

func (l *managedReplayLauncher) PrepareInput(context.Context, string, InputRequest) (*PreparedInput, error) {
	l.prepared.Add(1)
	return nil, nil
}

func (l *managedReplayLauncher) Run(ctx context.Context, start Start, s *Server) error {
	l.runs.Add(1)
	l.started <- s
	defer close(l.stopped)
	if l.ready != nil {
		select {
		case <-l.ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	id := start.Resume
	if start.Replay != nil {
		id = start.Replay.Session
	}
	if err := s.Publish(Frame{Session: Session{Thread: id}, Playback: &Playback{Speed: 2, Until: 1000}}); err != nil {
		return err
	}
	if err := s.SetSummary(Summary{Thread: id, Workspace: start.Workspace, Replay: start.Replay != nil, Ready: true}); err != nil {
		return err
	}
	for {
		select {
		case intent := <-s.Intents():
			l.intents <- intent
		case <-ctx.Done():
			close(l.canceled)
			if l.release != nil {
				<-l.release
			}
			return ctx.Err()
		}
	}
}

func TestManagedReplayValidationBeforeLiveAcquisition(t *testing.T) {
	t.Parallel()
	l := newManagedReplayLauncher()
	hub := NewHub(t.Context(), l)
	defer hub.Close()
	cases := []Start{
		{Workspace: "/workspace"}, {Model: "codex"}, {Effort: "high"},
		{Text: "message"}, {Resume: "other"}, {Attachments: []Attachment{{}}},
	}
	for _, start := range cases {
		start.Replay = &ReplayStart{Session: "saved"}
		if got := managedRequest(t, hub, "session/start", "", start); got.Code != 400 {
			t.Errorf("mixed fields %+v: %d %s", start, got.Code, got.Body)
		}
	}
	for _, id := range []string{"", " ", "../saved", `dir\saved`, "saved*", "saved?", "saved[0]"} {
		if got := managedRequest(t, hub, "session/start", "", Start{Replay: &ReplayStart{Session: id}}); got.Code != 400 {
			t.Errorf("ID %q: %d %s", id, got.Code, got.Body)
		}
	}
	for _, speed := range []float64{-1, 0, 0.09, 100.01} {
		if got := managedRequest(t, hub, "session/start", "", Start{Replay: &ReplayStart{Session: "saved", Speed: &speed}}); got.Code != 400 {
			t.Errorf("speed %g: %d %s", speed, got.Code, got.Body)
		}
	}
	if l.catalog.Load() != 0 || l.prepared.Load() != 0 || l.runs.Load() != 0 {
		t.Fatalf("invalid replay acquired live resources: catalog=%d prepared=%d runs=%d", l.catalog.Load(), l.prepared.Load(), l.runs.Load())
	}
}

func TestManagedReplayOwnerCollisions(t *testing.T) {
	t.Parallel()
	for _, ownerReplay := range []bool{false, true} {
		for _, pending := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "live", true: "replay"}[ownerReplay], map[bool]string{false: "ready", true: "pending"}[pending]}, "/"), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				l := newManagedReplayLauncher()
				if pending {
					l.ready = make(chan struct{})
				}
				hub := NewHub(ctx, l)
				defer hub.Close()
				live := Start{Workspace: t.TempDir(), Model: "codex", Resume: "saved"}
				replay := Start{Replay: &ReplayStart{Session: "saved"}}
				owner := live
				if ownerReplay {
					owner = replay
				}
				requestCtx, disconnect := context.WithCancel(ctx)
				defer disconnect()
				body, err := json.Marshal(owner)
				if err != nil {
					t.Fatal(err)
				}
				var response testReply
				returned := make(chan struct{})
				go func() {
					result, failure := hub.startSession(requestCtx, body)
					response = ownerReply(t, result, failure)
					close(returned)
				}()
				select {
				case <-l.started:
				case <-ctx.Done():
					t.Fatal("owner did not start")
				}
				if pending {
					disconnect()
				}
				<-returned
				if !pending && response.Code != 202 {
					t.Fatalf("owner: %d %s", response.Code, response.Body)
				}
				for _, duplicate := range []Start{replay, live} {
					got := managedRequest(t, hub, "session/start", "", duplicate)
					if got.Code != 409 || l.runs.Load() != 1 {
						t.Fatalf("collision: %d %s, runs=%d", got.Code, got.Body, l.runs.Load())
					}
				}
			})
		}
	}
}

func TestManagedReplaySharedPlaybackAndDisconnect(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	l := newManagedReplayLauncher()
	hub := NewHub(ctx, l)
	defer hub.Close()
	got := managedRequest(t, hub, "session/start", "", Start{Replay: &ReplayStart{Session: "saved"}})
	if got.Code != 202 {
		t.Fatalf("start: %d %s", got.Code, got.Body)
	}
	server := <-l.started
	if server.ActiveThread("saved") || l.catalog.Load() != 0 || l.prepared.Load() != 0 {
		t.Fatal("managed replay became a live attachment or acquired live resources")
	}
	for _, native := range []bool{false, true} {
		client := openTestRPC(t, hub.Handler(), native)
		_, err := client.Subscribe(ctx, "view/attach", RPCParams{Thread: "saved"})
		failure, ok := errors.AsType[*RPCError](err)
		if !ok || failure.Data.Status != 405 {
			t.Fatalf("replay view attachment: %v", err)
		}
		if result := client.call(t, "view/intent", RPCParams{Thread: "saved", Body: testJSON(t, map[string]any{})}); result.Code != 405 {
			t.Fatalf("replay view intent: %+v", result)
		}
		client.Close()
	}
	viewer := openTestRPC(t, hub.Handler(), false)
	subscription, err := viewer.Subscribe(ctx, "session/subscribe", RPCParams{Thread: "saved"})
	if err != nil {
		t.Fatal(err)
	}
	event, err := subscription.Next(ctx)
	viewer.Close()
	if err != nil || event.Kind != "snapshot" || !strings.Contains(string(event.Data), `"playback":{"position":0,"until":1000,"speed":2`) {
		t.Fatalf("playback stream: %+v, %v", event, err)
	}
	got = managedRequest(t, hub, "session/control", "saved", map[string]any{"action": Seek, "position": 250})
	if got.Code != 202 {
		t.Fatalf("control after disconnect: %d %s", got.Code, got.Body)
	}
	select {
	case intent := <-l.intents:
		if intent.Action != Seek || intent.Position != 250*time.Millisecond {
			t.Fatalf("playback intent: %+v", intent)
		}
	case <-ctx.Done():
		t.Fatal("disconnected viewer lost replay owner")
	}
	got = managedRequest(t, hub, "session/end", "saved", nil)
	if got.Code != 202 {
		t.Fatalf("end: %d %s", got.Code, got.Body)
	}
	select {
	case <-l.canceled:
	case <-ctx.Done():
		t.Fatal("end did not cancel replay")
	}
	hub.Close()
	select {
	case <-l.stopped:
	default:
		t.Fatal("end and Close did not join replay")
	}
}

func TestManagedReplayShutdownJoinsOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newManagedReplayLauncher()
		release := make(chan struct{})
		l.release = release
		hub := NewHub(t.Context(), l)
		got := managedRequest(t, hub, "session/start", "", Start{Replay: &ReplayStart{Session: "saved"}})
		if got.Code != 202 {
			t.Fatalf("start: %d %s", got.Code, got.Body)
		}
		closed := make(chan struct{})
		go func() { hub.Close(); close(closed) }()
		<-l.canceled
		synctest.Wait()
		select {
		case <-closed:
			t.Error("Close returned before replay released its resources")
		default:
		}
		close(release)
		<-closed
		if hub.activeThread("saved") || len(hub.running) != 0 || len(hub.sessions) != 0 {
			t.Fatal("joined replay retained ownership")
		}
	})
}

func TestManagedReplayNativeReservationSurvivesCurrentPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		launcher := newManagedLauncher()
		hub := NewHub(t.Context(), launcher)
		defer hub.Close()
		workspace := t.TempDir()
		started := managedRequest(t, hub, "session/start", "", Start{Workspace: workspace, Model: "codex", Resume: "current"})
		if started.Code != 202 {
			t.Fatal(started)
		}
		server := <-launcher.started
		if err := server.ReserveThread("saved"); err != nil {
			t.Fatal(err)
		}
		if err := server.SetSummary(Summary{Thread: "current", Workspace: workspace, Title: "Updated current thread", Ready: true}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		duplicate := managedRequest(t, hub, "session/start", "", Start{Replay: &ReplayStart{Session: "saved"}})
		if duplicate.Code != 409 {
			t.Fatalf("current-thread publication released pending native resume: %+v", duplicate)
		}
		if err := server.SetSummary(Summary{Thread: "saved", Workspace: workspace, Ready: true}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !server.ActiveThread("saved") || server.ActiveThread("current") {
			t.Fatal("confirmed replacement did not transfer registered ownership")
		}
		for runtime := range hub.running {
			if runtime.resume != "" {
				t.Fatal("confirmed replacement retained its pending reservation")
			}
		}
	})
}

type pendingNativeReplayLauncher struct{ *managedLauncher }

func (l pendingNativeReplayLauncher) Run(ctx context.Context, _ Start, server *Server) error {
	if err := server.ReserveThread("saved"); err != nil {
		return err
	}
	l.started <- server
	<-ctx.Done()
	return ctx.Err()
}

func TestManagedReplayRefusesPendingNativeStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	launcher := pendingNativeReplayLauncher{newManagedLauncher()}
	hub := NewHub(ctx, launcher)
	defer hub.Close()
	client := openTestRPC(t, hub.Handler(), true)
	finished := make(chan error, 1)
	workspace := t.TempDir()
	go func() {
		_, err := client.Subscribe(ctx, "native/start", RPCParams{Body: testJSON(t, NativeStart{Workspace: workspace, Args: []string{"resume", "saved"}})})
		finished <- err
	}()
	select {
	case <-launcher.started:
	case <-ctx.Done():
		t.Fatal("native startup did not reserve its selected thread")
	}
	if result := managedRequest(t, hub, "session/start", "", Start{Replay: &ReplayStart{Session: "saved"}}); result.Code != 409 {
		t.Fatalf("replay bypassed pending native ownership: %+v", result)
	}
	hub.Close()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
