//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type btwCodexRequest struct {
	body    []byte
	key     string
	headers http.Header
}

type btwCodexProvider struct {
	requests                       chan btwCodexRequest
	mainGate, sideGate, cancelGate chan struct{}
	canceled                       chan struct{}
}

func (p *btwCodexProvider) forwardExecution(ctx, responseCtx context.Context, body []byte, headers http.Header, key string) (*http.Response, error) {
	p.requests <- btwCodexRequest{bytes.Clone(body), key, headers.Clone()}
	var request struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	prompt := ""
	for _, item := range request.Input {
		if item.Role == "user" {
			prompt = ""
			for _, part := range item.Content {
				prompt += part.Text
			}
		}
	}
	if strings.Contains(prompt, "Main running") {
		select {
		case <-p.mainGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	response := routerFaultCodexSuccessResponse()
	wire, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	answer := "Main answer"
	var gate <-chan struct{}
	if strings.Contains(prompt, "Side question") {
		answer, gate = "Side streaming answer", p.sideGate
	}
	if strings.Contains(prompt, "Side followup") {
		answer = "Side followup answer"
	}
	if strings.Contains(prompt, "Side cancel") {
		answer, gate = "Side cancel streaming", p.cancelGate
	}
	wire = bytes.ReplaceAll(wire, []byte("Recovered after a retry."), []byte(answer))
	if gate == nil {
		response.Body = io.NopCloser(bytes.NewReader(wire))
		return response, nil
	}
	// Deliver a real streamed delta, then withhold finalization until the test
	// has inspected the rendered terminal or canceled only this side answer.
	marker := bytes.Index(wire, []byte(`"type":"response.output_text.done"`))
	if marker < 0 {
		panic("missing completion boundary in SSE fixture")
	}
	cut := bytes.LastIndex(wire[:marker], []byte("data: "))
	reader, writer := io.Pipe()
	response.Body = reader
	go func() {
		defer writer.Close()
		if _, err := writer.Write(wire[:cut]); err != nil {
			return
		}
		select {
		case <-gate:
			_, _ = writer.Write(wire[cut:])
		case <-responseCtx.Done():
			if strings.Contains(prompt, "Side cancel") {
				close(p.canceled)
			}
			_ = writer.CloseWithError(responseCtx.Err())
		}
	}()
	return response, nil
}

func TestAppServerBTWNativeCodexE2E(t *testing.T) {
	for _, scenario := range []string{"idle", "active", "resumed"} {
		t.Run(scenario, func(t *testing.T) {
			active := scenario == "active"
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			codex, err := exec.LookPath("codex")
			if err != nil {
				t.Fatal(err)
			}
			p := &btwCodexProvider{requests: make(chan btwCodexRequest, 12), mainGate: make(chan struct{}), sideGate: make(chan struct{}), cancelGate: make(chan struct{}), canceled: make(chan struct{})}
			proxy := newManagedMekugiProxy(t)
			server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, p, nil, proxy, nil))
			defer server.Close()
			defer func() {
				for _, gate := range []chan struct{}{p.mainGate, p.sideGate, p.cancelGate} {
					select {
					case <-gate:
					default:
						close(gate)
					}
				}
			}()
			environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
			command := func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}
			terminal := startAppResumeTerminalWithProxy(t, command, "", proxy)
			next := func() btwCodexRequest {
				t.Helper()
				select {
				case r := <-p.requests:
					return r
				case <-terminal.ctx.Done():
					t.Fatalf("missing request: %v\n%s", terminal.ctx.Err(), terminal.screen.String())
				}
				return btwCodexRequest{}
			}
			terminal.await("Ready")
			terminal.send("/model gpt-6-sol\r")
			terminal.await("gpt-6-sol")
			terminal.send("/reasoning high\r")
			terminal.await("gpt-6-sol (high)")
			terminal.send("Main seed\r")
			main := next()
			terminal.await("completed")
			if scenario == "resumed" {
				metadata, _ := decodeCodexTurnMetadata(main.headers)
				terminal.stopCanceled()
				terminal = startAppResumeTerminalWithProxy(t, command, metadata.ThreadID, proxy)
				terminal.await("Ready")
				terminal.send("Main resumed\r")
				main = next()
				terminal.await("completed")
			}
			if active {
				terminal.send("Main running\r")
				running := next()
				if running.key != main.key {
					t.Fatal("main cache identity changed before /btw")
				}
				terminal.await("Working")
			}
			terminal.send("/btw Side question\r")
			side := next()
			mainMetadata, _ := decodeCodexTurnMetadata(main.headers)
			sideMetadata, _ := decodeCodexTurnMetadata(side.headers)
			if side.key == "" || main.key == "" || sideMetadata.ThreadID == "" || sideMetadata.ThreadID == mainMetadata.ThreadID {
				t.Fatalf("fork identities: main=%q side=%q", mainMetadata.ThreadID, sideMetadata.ThreadID)
			}
			var mainSettings, sideSettings struct {
				Model     string `json:"model"`
				Reasoning struct {
					Effort string `json:"effort"`
				} `json:"reasoning"`
			}
			if err := json.Unmarshal(main.body, &mainSettings); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(side.body, &sideSettings); err != nil {
				t.Fatal(err)
			}
			if mainSettings != sideSettings {
				t.Fatalf("side did not inherit live settings: main=%+v side=%+v", mainSettings, sideSettings)
			}
			if !bytes.Contains(side.body, []byte("Main seed")) || active && !bytes.Contains(side.body, []byte("Main running")) {
				t.Fatal("branch omitted the current conversation")
			}
			terminal.await("Side streaming answer")
			if active && !strings.Contains(terminal.screen.String(), "Working") {
				t.Fatal("side stream changed main turn state")
			}
			close(p.sideGate)
			terminal.await("/btw · completed")
			terminal.send("/btw Side followup\r")
			followup := next()
			if followup.key != side.key || !bytes.Contains(followup.body, []byte("Side question")) || !bytes.Contains(followup.body, []byte("Side streaming answer")) {
				t.Fatal("follow-up did not retain its side context and identity")
			}
			terminal.await("Side followup answer")
			terminal.await("/btw · completed")
			if active {
				terminal.send("/btw Side cancel\r")
				next()
				terminal.await("Side cancel streaming")
			}
			terminal.send("kept draft\x1b")
			terminal.awaitMatch("dock closed, draft kept", func(frame string) bool {
				return !strings.Contains(frame, "╭─ /btw") && strings.Contains(frame, "kept draft")
			})
			if active {
				select {
				case <-p.canceled:
				case <-terminal.ctx.Done():
					t.Fatal("closing side did not cancel its request")
				}
				if !strings.Contains(terminal.screen.String(), "Working") {
					t.Fatal("Escape interrupted Main")
				}
				close(p.mainGate)
				terminal.await("completed")
			}
			terminal.send("\x03Main after\r")
			after := next()
			if after.key != main.key || bytes.Contains(after.body, []byte("Side question")) || bytes.Contains(after.body, []byte("Side followup")) {
				t.Fatal("/btw polluted Main history/cache identity")
			}
			var beforeFields, afterFields map[string]any
			if err := json.Unmarshal(main.body, &beforeFields); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(after.body, &afterFields); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"instructions", "tools", "model", "prompt_cache_key"} {
				if !reflect.DeepEqual(beforeFields[field], afterFields[field]) {
					t.Fatalf("/btw changed Main's cache-prefix field %s", field)
				}
			}
			terminal.await("Main after")
			terminal.await("completed")
			terminal.quit()
		})
	}
}
