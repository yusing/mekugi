package launch

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

func TestServerReplayOwnedRuntime(t *testing.T) {
	directory, state := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", directory)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // Playback must never need a host or provider executable.
	sessions := filepath.Join(directory, "sessions")
	if err := os.Mkdir(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	path := replayTestWrite(t, sessions, "rollout-main.jsonl", replayTestMeta("main"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("main", "turn", replayTestEpoch+1000, replayTestEpoch+2000, map[string]any{"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": "Offline replay."}}}),
		replayTestRecord("event_msg", replayTestEpoch+50000, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	hub := sessionapi.NewHub(ctx, &serverLauncher{})
	host := httptest.NewServer(sessionapi.Versioned(hub.Handler()))
	defer func() { cancel(); hub.Close(); host.Close() }()
	connect := func() *sessionapi.RPCClient {
		t.Helper()
		rpc, err := sessionapi.OpenRPC(ctx, host.Client(), host.URL+sessionapi.RPCPath, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rpc.Close() })
		return rpc
	}
	rpc := connect()
	create := func() {
		t.Helper()
		body := serverRPCCall(t, rpc, "session/start", "", sessionapi.Start{Replay: &sessionapi.ReplayStart{Session: "main", Seed: new(uint64(0)), ChildReplySummary: new(false)}})
		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.ID != "main" {
			t.Fatalf("historical identity: %+v, %v", result, err)
		}
	}
	create()
	native, err := sessionapi.OpenRPC(ctx, host.Client(), host.URL+sessionapi.NativeRPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	nativeBody, err := json.Marshal(sessionapi.NativeStart{Workspace: t.TempDir(), Args: []string{"resume", "main"}})
	if err != nil {
		t.Fatal(err)
	}
	startup, err := native.Subscribe(ctx, "native/start", sessionapi.RPCParams{Body: nativeBody})
	if err != nil {
		t.Fatal(err)
	}
	exit, err := startup.Next(ctx)
	var outcome sessionapi.NativeExit
	if err != nil || exit.Kind != "exit" || json.Unmarshal(exit.Data, &outcome) != nil || !strings.Contains(outcome.Message, "already running") {
		t.Fatalf("native replay collision reached host startup: %+v, %+v, %v", exit, outcome, err)
	}
	native.Close()
	events := serverRPCSubscribe(t, rpc, "main")
	serverCLIEvent(t, events, "snapshot", `"workspace":"/workspace/replay"`)
	serverRPCCall(t, rpc, "session/control", "main", map[string]any{"action": "seek", "position": 100000})
	serverCLIEvent(t, events, "patch", `"paused":true`)
	// Completion retains the owned playback and settled content for reconnects.
	serverRPCCall(t, rpc, "session/read", "main", nil)
	rpc.Close()
	rpc = connect()
	events = serverRPCSubscribe(t, rpc, "main")
	serverCLIEvent(t, events, "snapshot", "Offline replay.")
	serverRPCCall(t, rpc, "session/control", "main", map[string]string{"action": "restart"})
	serverCLIEvent(t, events, "snapshot", `"position":0`)
	serverRPCCall(t, rpc, "session/control", "main", map[string]string{"action": "faster"})
	serverCLIEvent(t, events, "patch", `"speed":2`)
	events.Close()
	serverRPCCall(t, rpc, "session/end", "main", nil)
	serverCLIAwait(t, func() bool {
		err := rpc.Call(ctx, "session/read", sessionapi.RPCParams{Thread: "main"}, nil)
		failure, ok := errors.AsType[*sessionapi.RPCError](err)
		return ok && failure.Data.Status == 404
	})
	// Failed source admission releases its pending reservation as well.
	for range 2 {
		err := rpc.Call(ctx, "session/start", sessionapi.RPCParams{Body: []byte(`{"replay":{"session":"missing"}}`)}, nil)
		if failure, ok := errors.AsType[*sessionapi.RPCError](err); !ok || failure.Data.Status != 502 {
			t.Fatalf("missing replay: %v", err)
		}
	}
	// The previous owner was retired, so the same recording can be admitted again.
	create()
	hub.Close()
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("playback wrote retained state: %v, %v", entries, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("playback changed its rollout: %v", err)
	}
}

func TestServerReplayNativeSwitchReservation(t *testing.T) {
	u, wire := newResumeSessionTestUI(t)
	reserved := ""
	refused := errors.New("That thread is already running.")
	u.attached.reserveThread = func(thread string) error {
		if thread == "blocked" {
			return refused
		}
		reserved = thread
		return nil
	}
	if err := resumeSession(&u.attached, u.shell, &u.Client, &u.picker, u.clock, &u.resumePicker, u.approvalMode, "blocked"); err != nil || !strings.Contains(u.attached.feedback.Text, refused.Error()) || wire.Len() != 0 || u.Replacement.Pending() {
		t.Fatalf("blocked resume reached host or changed pending replacement: %v, %s", err, wire.String())
	}
	if err := resumeSession(&u.attached, u.shell, &u.Client, &u.picker, u.clock, &u.resumePicker, u.approvalMode, "saved"); err != nil || reserved != "saved" {
		t.Fatalf("saved resume did not reserve before host request: %q, %v", reserved, err)
	}
	read := resumeTestOne(t, wire, "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, read.ID))
	if reserved != "" || u.Replacement.Pending() {
		t.Fatalf("failed native switch retained reservation: %q", reserved)
	}
}

func TestServerReplayControlsAndDetachedAttachment(t *testing.T) {
	t.Parallel()
	conversation := new(conversationmodel.Service)
	observations := conversationbinding.Codex.Observation(conversation)
	p := &serverReplay{Player: playback.NewPlayer(replayPlaybackTestSource(), 1), ctx: t.Context(),
		replayOwners: replayOwners{testPresentation(), conversation, &observations, new(activitymodel.Service)}}
	p.reset()
	defer func() { p.current.session.Retire() }()
	if err := p.control(sessionapi.Intent{Action: sessionapi.Seek, Position: 10 * time.Second}, 0); err != nil {
		t.Fatal(err)
	}
	completed := p.frame()
	if p.current.shell != nil || p.current.view != nil || p.current.agents != nil {
		t.Fatal("browser replay constructed terminal consumers")
	}
	if completed.Input != nil || completed.Prompts != nil || !completed.Playback.Simulated || !strings.Contains(completed.Main.Entries[len(completed.Main.Entries)-1].Text, "Repaint diagnosis complete.") {
		t.Fatalf("settled replay frame: %+v", completed)
	}
	output := ""
	for _, entry := range completed.Main.Entries {
		for _, value := range entry.Values {
			output += strings.Join(value.Tail, "\n")
		}
	}
	if !strings.Contains(output, "captured output") {
		t.Fatalf("recorded output was not hydrated for the browser: %q", output)
	}
	if err := p.control(sessionapi.Intent{Action: sessionapi.Faster}, time.Second); err != nil || p.Position != 11*time.Second || p.Speed != 2 {
		t.Fatalf("elapsed prior speed: %s, %g, %v", p.Position, p.Speed, err)
	}
	if err := p.control(sessionapi.Intent{Action: sessionapi.TogglePause}, time.Second); err != nil || p.Position != 13*time.Second || !p.Paused {
		t.Fatalf("pause elapsed: %s, %v, %v", p.Position, p.Paused, err)
	}
	if err := p.control(sessionapi.Intent{Action: sessionapi.SeekBack}, time.Hour); err != nil || p.Position != 3*time.Second {
		t.Fatalf("paused seek: %s, %v", p.Position, err)
	}
	reset := p.frame()
	if reset.Attachment == completed.Attachment || len(reset.Main.Entries) >= len(completed.Main.Entries) {
		t.Fatal("backward seek retained completed attachment state")
	}
	if err := p.control(sessionapi.Intent{Action: sessionapi.Restart}, 0); err != nil || p.Position != 0 || p.Paused {
		t.Fatalf("restart: %s, %v, %v", p.Position, p.Paused, err)
	}
}
