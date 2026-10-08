//go:build journal_e2e

package router

import (
	"bufio"
	"bytes"
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/capturer"
)

type btwPrewarmRequest struct {
	prewarm bool
	body    []byte
}

type btwPrewarmProvider struct {
	requests chan btwPrewarmRequest
	release  chan struct{}
}

func builtInOpenAICodexEnvironment(t *testing.T) []string {
	t.Helper()
	environment := routerFaultCodexEnvironment(t)
	isolated := environment[:0]
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY", "CHATGPT_API_KEY":
			continue
		case "CODEX_HOME":
			if err := os.WriteFile(filepath.Join(value, "auth.json"), []byte(`{"OPENAI_API_KEY":"sk-local-openai-fixture"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		isolated = append(isolated, entry)
	}
	return isolated
}

func (p *btwPrewarmProvider) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "fixture requires upstream WebSockets", http.StatusBadRequest)
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
		p.requests <- btwPrewarmRequest{prewarm: prewarm, body: body}
		if prewarm {
			if providerSocketWrite(r.Context(), conn, socketEvent("response.created", "warm")) != nil ||
				providerSocketWrite(r.Context(), conn, socketEvent("response.completed", "warm")) != nil {
				return
			}
			continue
		}
		select {
		case <-p.release:
		case <-r.Context().Done():
			return
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

func TestAppServerBuiltInOpenAINativeCodexE2E(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "websocket"
		if fallback {
			name = "http_fallback"
		}
		t.Run(name, func(t *testing.T) { testAppServerBuiltInOpenAI(t, fallback) })
	}
}

func testAppServerBuiltInOpenAI(t *testing.T, fallback bool) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	provider := &btwPrewarmProvider{requests: make(chan btwPrewarmRequest, 32), release: make(chan struct{})}
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
	downstream := make(chan string, 32)
	router := httptest.NewServer(capture.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deterministic fixture credentials terminate at the local mock.
		for key, values := range codexAuthHeaders() {
			r.Header[key] = values
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			downstream <- "ws"
			if fallback {
				http.Error(w, "fixture requires HTTP", http.StatusUpgradeRequired)
				return
			}
			ws.ServeHTTP(w, r)
		} else {
			downstream <- "http"
			httpResponses.ServeHTTP(w, r)
		}
	})))
	defer router.Close()
	environment, workspace := builtInOpenAICodexEnvironment(t), t.TempDir()
	if output, err := exec.Command("git", "init", "--quiet", workspace).CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture workspace: %v: %s", err, output)
	}
	command := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `openai_base_url=`+strconv.Quote(router.URL+"/v1"), "-c", `model_provider="openai"`, "-c", `model_reasoning_effort="low"`, "-c", `web_search="live"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}
	terminal := startAppResumeTerminalWithProxy(t, command, "", proxy, capture)
	if err := pty.Setsize(terminal.outer, &pty.Winsize{Cols: 200, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	// Release a blocked inference before closing either HTTP server on failure.
	defer func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
	}()
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
	prewarms := 0
	webExposed := false
	awaitPrompt := func(prompt string) btwPrewarmRequest {
		t.Helper()
		for {
			request := next()
			webExposed = webExposed || bytes.Contains(request.body, []byte("web__run(args:"))
			if request.prewarm {
				prewarms++
				continue
			}
			if !bytes.Contains(request.body, []byte(prompt)) {
				t.Fatalf("provider request missing %q: %s", prompt, request.body)
			}
			return request
		}
	}
	terminal.await("Ready")
	terminal.send("Main seed\r")
	first := awaitPrompt("Main seed")
	if !webExposed {
		t.Fatal("callable web__run declaration missing from the prepared catalog")
	}
	var firstRequest struct {
		ClientMetadata map[string]string `json:"client_metadata"`
		Input          []struct {
			Metadata struct {
				TurnID string `json:"turn_id"`
			} `json:"internal_chat_message_metadata_passthrough"`
		} `json:"input"`
	}
	if err := json.Unmarshal(first.body, &firstRequest); err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	for key, value := range firstRequest.ClientMetadata {
		headers.Set(key, value)
	}
	metadata, valid := decodeCodexTurnMetadata(headers)
	directory, selected := usableRoutingDirectory(metadata.Directories)
	if !valid || !selected || directory != workspace {
		t.Fatalf("selected workspace metadata missing: valid=%t selected=%t directory=%q want=%q", valid, selected, directory, workspace)
	}
	thread := metadata.ThreadID
	if thread == "" {
		thread = headers.Get(threadIDHeader)
	}
	if _, err := proxy.journals.list(terminal.ctx, proxy.replayStore, workspace, thread); err != nil {
		t.Fatalf("journal not stored in the selected workspace: %v", err)
	}
	metadataFound := false
	for _, item := range firstRequest.Input {
		metadataFound = metadataFound || item.Metadata.TurnID != ""
	}
	if !metadataFound {
		t.Fatal("host-authored internal turn metadata missing")
	}
	terminal.await("Working")
	terminal.send("/effort\r")
	terminal.await("Choose effort")
	terminal.await("enter apply")
	terminal.send("\x1b[B\x1b[B\r")
	terminal.await("live update published")
	close(provider.release)
	terminal.await("completed")
	terminal.send("Settings followup\r")
	updated := awaitPrompt("Settings followup")
	var request struct {
		Input []struct {
			Type      string `json:"type"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		} `json:"input"`
	}
	if err := json.Unmarshal(updated.body, &request); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range request.Input {
		if item.Type == "configuration_update" && item.Reasoning.Effort == "high" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing host high-effort configuration_update: %s", updated.body)
	}
	terminal.await("Settings followup")
	terminal.await("completed")
	if fallback {
		select {
		case transport := <-downstream:
			if transport != "ws" {
				t.Fatalf("first transport=%s", transport)
			}
		default:
			t.Fatal("missing WS attempt")
		}
		select {
		case transport := <-downstream:
			if transport != "http" {
				t.Fatalf("fallback transport=%s", transport)
			}
		default:
			t.Fatal("missing HTTP fallback")
		}
		terminal.quit()
		return
	}
	if prewarms == 0 {
		t.Fatal("built-in OpenAI did not prewarm through router")
	}
	terminal.send("/btw Side question\r")
	awaitPrompt("Side question")
	terminal.await("/btw · completed")
	terminal.send("/btw Side followup\r")
	awaitPrompt("Side followup")
	terminal.await("Side followup")
	terminal.await("/btw · completed")
	terminal.send("\x1b")
	terminal.awaitMatch("side closed", func(frame string) bool { return !strings.Contains(frame, "╭─ /btw") })
	terminal.send("/btw New side\r")
	awaitPrompt("New side")
	terminal.await("/btw · completed")
	terminal.send("\x1b")
	terminal.awaitMatch("side closed", func(frame string) bool { return !strings.Contains(frame, "╭─ /btw") })
	terminal.send("Main after\r")
	awaitPrompt("Main after")
	terminal.await("Main after")
	terminal.await("completed")
	terminal.quit()
}
