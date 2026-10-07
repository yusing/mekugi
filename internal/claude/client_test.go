package claude

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/session"
)

func startMockBridge(t *testing.T, ctx context.Context, script string, config Config) *Client {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for mock bridge transport tests")
	}
	path := filepath.Join(t.TempDir(), "mock bridge.cjs")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := Start(ctx, node, path, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.cancel()
		_ = client.Close()
	})
	return client
}

func nextEvent(t *testing.T, client *Client) session.Event {
	t.Helper()
	select {
	case event, ok := <-client.Events():
		if !ok {
			t.Fatal("bridge event stream closed unexpectedly")
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mock bridge event")
		return session.Event{}
	}
}

func TestClientExactConfigurationAndInput(t *testing.T) {
	config := Config{Cwd: t.TempDir(), Executable: "/path with spaces/claude", Resume: "session-id", ForkSession: true, Model: "claude-model"}
	client := startMockBridge(t, t.Context(), `
const readline = require('node:readline');
console.log(JSON.stringify({kind: 'error', text: JSON.stringify({config: JSON.parse(process.argv[2]), cwd: process.cwd()})}));
readline.createInterface({input: process.stdin}).on('line', line => {
  console.log(JSON.stringify({kind: 'error', text: line}));
});
`, config)
	var startup struct {
		Config Config `json:"config"`
		Cwd    string `json:"cwd"`
	}
	if err := json.Unmarshal([]byte(nextEvent(t, client).Text), &startup); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(startup.Config, config) || startup.Cwd != config.Cwd {
		t.Fatalf("startup = %#v, want config %#v and matching working directory", startup, config)
	}
	text := "line one\nline two\t\"quotes\" \\ $HOME `command` $(command) 日本語"
	if err := client.Send(t.Context(), text); err != nil {
		t.Fatal(err)
	}
	var input struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(nextEvent(t, client).Text), &input); err != nil {
		t.Fatal(err)
	}
	if input.Kind != "input" || input.Text != text {
		t.Fatalf("input = %#v, want exact text %q", input, text)
	}
	decision := session.Decision{ID: "permission-1", Allow: false, Answers: map[string]string{"Which backend?": "Claude\nOther"}}
	if err := client.Respond(t.Context(), decision); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Kind    string            `json:"kind"`
		ID      string            `json:"id"`
		Allow   bool              `json:"allow"`
		Answers map[string]string `json:"answers"`
	}
	if err := json.Unmarshal([]byte(nextEvent(t, client).Text), &response); err != nil {
		t.Fatal(err)
	}
	if response.Kind != "decision" || response.ID != decision.ID || response.Allow != decision.Allow || !reflect.DeepEqual(response.Answers, decision.Answers) {
		t.Fatalf("decision response = %#v, want %#v", response, decision)
	}
	if err := client.Respond(t.Context(), session.Decision{ID: "ordinary-permission", Allow: true}); err != nil {
		t.Fatal(err)
	}
	var ordinary map[string]any
	if err := json.Unmarshal([]byte(nextEvent(t, client).Text), &ordinary); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ordinary, map[string]any{"kind": "decision", "id": "ordinary-permission", "allow": true}) {
		t.Fatalf("ordinary permission frame = %#v, want allow without native argument rewrites", ordinary)
	}
	if err := client.Interrupt(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := nextEvent(t, client).Text; got != `{"kind":"interrupt"}` {
		t.Fatalf("interrupt frame = %q", got)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("repeated Close = %v", err)
	}
	if _, ok := <-client.Events(); ok {
		t.Fatal("events channel remains open after Close")
	}
}

func TestClientIsolatesOnlyFreshCommandTrackingGuard(t *testing.T) {
	t.Setenv(execsegment.Guard, "1")
	t.Setenv("MEKUGI_NATIVE_PARENT_VALUE", "preserved")
	for _, tracking := range []bool{false, true} {
		t.Run(fmt.Sprint(tracking), func(t *testing.T) {
			config := Config{Cwd: t.TempDir()}
			wantParent := "preserved"
			if tracking {
				config.Companion = &ObservationEndpoint{BashEnv: "/launch/private/bash-env"}
				config.Environment = append(os.Environ(), "MEKUGI_NATIVE_PARENT_VALUE=invocation")
				wantParent = "invocation"
			}
			client := startMockBridge(t, t.Context(), `
console.log(JSON.stringify({kind:'notice', text:JSON.stringify({guard:process.env.MEKUGI_EXEC_TRACK ?? null, parent:process.env.MEKUGI_NATIVE_PARENT_VALUE})}));
process.stdin.resume();
`, config)
			var environment struct {
				Guard  *string `json:"guard"`
				Parent string  `json:"parent"`
			}
			if err := json.Unmarshal([]byte(nextEvent(t, client).Text), &environment); err != nil {
				t.Fatal(err)
			}
			if environment.Parent != wantParent || tracking && environment.Guard != nil || !tracking && (environment.Guard == nil || *environment.Guard != "1") {
				t.Fatalf("wrong native launch environment: %+v", environment)
			}
			if os.Getenv(execsegment.Guard) != "1" || os.Getenv("MEKUGI_NATIVE_PARENT_VALUE") != "preserved" {
				t.Fatal("launch changed caller environment")
			}
		})
	}
}

func TestClientRejectsOversizedAndCancelledInput(t *testing.T) {
	client := startMockBridge(t, t.Context(), `process.stdin.resume(); console.log('{"kind":"ready"}');`, Config{Cwd: t.TempDir()})
	if event := nextEvent(t, client); event.Kind != "ready" {
		t.Fatalf("first event = %#v", event)
	}
	if err := client.Send(t.Context(), strings.Repeat("x", frameLimit)); err == nil {
		t.Fatal("oversized input was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, call := range map[string]func() error{
		"send":      func() error { return client.Send(ctx, "ignored") },
		"respond":   func() error { return client.Respond(ctx, session.Decision{}) },
		"interrupt": func() error { return client.Interrupt(ctx) },
	} {
		if err := call(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s error = %v, want context cancellation", name, err)
		}
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsForkWithoutResumeBeforeLaunch(t *testing.T) {
	c, err := Start(t.Context(), "must-not-execute", "must-not-open", Config{ForkSession: true})
	if c != nil || err == nil || !strings.Contains(err.Error(), "resume session ID") {
		t.Fatalf("unbound native fork accepted: %v", err)
	}
}

func TestClientProtocolFailuresAreVisible(t *testing.T) {
	for name, script := range map[string]string{
		"malformed":    `console.log('{'); process.stdin.resume();`,
		"oversized":    `console.log('x'.repeat(8 * 1024 * 1024)); process.stdin.resume();`,
		"nonzero exit": `process.exitCode = 7;`,
	} {
		t.Run(name, func(t *testing.T) {
			client := startMockBridge(t, t.Context(), script, Config{Cwd: t.TempDir()})
			event := nextEvent(t, client)
			if event.Kind != "error" || event.Text == "" {
				t.Errorf("failure event = %#v, want visible error", event)
			}
			if err := client.Close(); err == nil {
				t.Fatal("failed bridge Close did not preserve the transport error")
			}
		})
	}
}

func TestClientCancellationUnblocksFullEventQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := startMockBridge(t, ctx, `for (let i = 0; i < 1000; i++) console.log('{"kind":"ready"}'); process.stdin.resume();`, Config{Cwd: t.TempDir()})
	if event := nextEvent(t, client); event.Kind != "ready" {
		t.Fatalf("first event = %#v", event)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(client.events) < cap(client.events) {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("mock bridge did not fill the event queue")
		}
	}
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not unblock event delivery and reap bridge")
	}
	for range client.Events() {
	}
}

func TestClientCompanionCapabilityUsesPrivatePipe(t *testing.T) {
	endpoint := &ObservationEndpoint{Socket: "/private/socket", Token: "private-connection-capability"}
	client := startMockBridge(t, t.Context(), `
const fs = require('node:fs');
const config = JSON.parse(process.argv[2]);
const endpoint = JSON.parse(fs.readFileSync(config.companionFD, 'utf8'));
fs.closeSync(config.companionFD);
console.log(JSON.stringify({kind:'notice',text: endpoint.socket === '/private/socket' && endpoint.token === 'private-connection-capability' ? 'capability received privately' : 'bad capability'}));
process.stdin.resume();
`, Config{Cwd: t.TempDir(), Companion: endpoint})
	if strings.Contains(strings.Join(client.cmd.Args, " "), endpoint.Token) {
		t.Fatal("capability exposed in process arguments")
	}
	if event := nextEvent(t, client); event.Text != "capability received privately" {
		t.Fatal("private configuration pipe failed")
	}
}
