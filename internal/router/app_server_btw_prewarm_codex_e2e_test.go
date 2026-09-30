//go:build journal_e2e

package router

import (
	"bufio"
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/capturer"
)

type btwPrewarmRequest struct {
	transport string
	prewarm   bool
	body      []byte
}

type btwPrewarmProvider struct{ requests chan btwPrewarmRequest }

// This mock deliberately accepts both provider transports. An HTTP-only mock
// cannot detect a fork that silently creates an expensive WebSocket prewarm.
func (p *btwPrewarmProvider) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.requests <- btwPrewarmRequest{transport: "http", prewarm: bytes.Contains(body, []byte(`"generate":false`)), body: body}
		response := routerFaultCodexSuccessResponse()
		defer response.Body.Close()
		for k, values := range response.Header {
			w.Header()[k] = values
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	for {
		fields, err := providerSocketRead(r.Context(), conn)
		if err != nil {
			return
		}
		body, err := json.Marshal(&fields)
		if err != nil {
			return
		}
		prewarm := string(fields["generate"]) == "false"
		p.requests <- btwPrewarmRequest{transport: "ws", prewarm: prewarm, body: body}
		if prewarm {
			if providerSocketWrite(r.Context(), conn, socketEvent("response.created", "warm")) != nil ||
				providerSocketWrite(r.Context(), conn, socketEvent("response.completed", "warm")) != nil {
				return
			}
			continue
		}
		response := routerFaultCodexSuccessResponse()
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			line := scanner.Bytes()
			if !bytes.HasPrefix(line, []byte("data: ")) {
				continue
			}
			var event map[string]any
			if json.Unmarshal(line[len("data: "):], &event) != nil || providerSocketWrite(r.Context(), conn, event) != nil {
				response.Body.Close()
				return
			}
		}
		response.Body.Close()
		if scanner.Err() != nil {
			return
		}
	}
}

func TestAppServerNoModelPrewarmNativeCodexE2E(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	provider := &btwPrewarmProvider{requests: make(chan btwPrewarmRequest, 20)}
	upstream := httptest.NewServer(http.HandlerFunc(provider.serve))
	defer upstream.Close()
	capture, err := capturer.New(capturer.Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	proxy := newManagedMekugiProxy(t)
	// Match production's durable journal/replay owner rather than a memory-only
	// fixture whose early export-only reads can leave a phantom empty journal.
	attachTestReplayStore(t, proxy)
	providerClient := newProviderClient(upstream.URL, upstream.Client())
	providerClient.enableWebSockets(t.Context())
	defer providerClient.websockets.close()
	ws := responsesWebSocketHandler(t.Context(), time.Minute, providerClient, nil, proxy)
	defer ws.Close()
	httpResponses := responsesHandler(t.Context(), time.Minute, providerClient, nil, proxy)
	downstream := make(chan string, 10)
	router := httptest.NewServer(capture.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deterministic fixture credentials terminate at the local mock.
		for key, values := range codexAuthHeaders() {
			r.Header[key] = values
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			downstream <- "ws"
			ws.ServeHTTP(w, r)
		} else {
			downstream <- "http"
			httpResponses.ServeHTTP(w, r)
		}
	})))
	defer router.Close()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	command := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="OpenAI",base_url=`+strconv.Quote(router.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false,supports_websockets=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}
	terminal := startAppResumeTerminalWithProxy(t, command, "", proxy, capture)
	next := func() btwPrewarmRequest {
		t.Helper()
		select {
		case request := <-provider.requests:
			return request
		case <-terminal.ctx.Done():
			t.Fatalf("missing provider request: %v\n%s", terminal.ctx.Err(), terminal.screen.String())
			return btwPrewarmRequest{}
		}
	}
	nextDownstream := func() string {
		t.Helper()
		select {
		case transport := <-downstream:
			return transport
		case <-terminal.ctx.Done():
			t.Fatalf("missing Codex request: %v\n%s", terminal.ctx.Err(), terminal.screen.String())
			return ""
		}
	}
	awaitPrompt := func(prompt, transport string) btwPrewarmRequest {
		t.Helper()
		for {
			request := next()
			if request.prewarm {
				t.Fatalf("unexpected prewarm before %q: %s", prompt, request.transport)
			}
			if !bytes.Contains(request.body, []byte(prompt)) || request.transport != transport {
				t.Fatalf("request for %q: transport=%s body=%.300s", prompt, request.transport, request.body)
			}
			return request
		}
	}
	terminal.await("Ready")
	terminal.send("Main seed\r")
	if got := nextDownstream(); got != "http" {
		t.Fatalf("Main transport = %s, want HTTP", got)
	}
	awaitPrompt("Main seed", "ws")
	terminal.await("completed")
	terminal.send("/btw Side question\r")
	if got := nextDownstream(); got != "http" {
		t.Fatalf("side transport = %s, want HTTP", got)
	}
	awaitPrompt("Side question", "ws")
	terminal.await("/btw · completed")
	terminal.send("/btw Side followup\r")
	if got := nextDownstream(); got != "http" {
		t.Fatalf("follow-up transport = %s, want HTTP", got)
	}
	awaitPrompt("Side followup", "ws")
	terminal.await("Side followup")
	terminal.await("/btw · completed")
	terminal.send("\x1b")
	terminal.awaitMatch("side closed", func(frame string) bool { return !strings.Contains(frame, "╭─ /btw") })
	terminal.send("/btw New side\r")
	if got := nextDownstream(); got != "http" {
		t.Fatalf("new side transport = %s, want HTTP", got)
	}
	awaitPrompt("New side", "ws")
	terminal.await("/btw · completed")
	terminal.send("\x1b")
	terminal.awaitMatch("side closed", func(frame string) bool { return !strings.Contains(frame, "╭─ /btw") })
	terminal.send("Main after\r")
	if got := nextDownstream(); got != "http" {
		t.Fatalf("Main transport after side questions = %s, want HTTP", got)
	}
	awaitPrompt("Main after", "ws")
	terminal.await("Main after")
	terminal.await("completed")
	terminal.send("/session\r")
	terminal.await("Session · Overview")
	terminal.awaitMatch("launch counts", func(frame string) bool {
		return strings.Contains(frame, "Logical") && strings.Contains(frame, "All threads in this launch")
	})
	if got := capture.Snapshot().Requests.Logical; got != 5 {
		t.Fatalf("launch captured %d requests, want five turns without prewarm", got)
	}
	terminal.send("\x1b[C\x1b[C")
	terminal.await("Session · Exchanges")
	terminal.send("\x1b")
	terminal.awaitMatch("session dialog closed", func(frame string) bool { return !strings.Contains(frame, "Session · Exchanges") })
	select {
	case request := <-provider.requests:
		t.Fatalf("unexpected extra provider request: transport=%s prewarm=%t", request.transport, request.prewarm)
	default:
	}
	select {
	case got := <-downstream:
		t.Fatalf("unexpected extra Codex request: %s", got)
	default:
	}
	terminal.quit()
}
