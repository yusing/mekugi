//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

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

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
)

type btwCodexRequest struct {
	body    []byte
	key     string
	headers http.Header
}

func TestAgentQuestionNativeCodex(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	codex, err := exec.LookPath("mekugi-app-server")
	if err != nil {
		t.Fatal(err)
	}
	p := &btwCodexProvider{requests: make(chan btwCodexRequest, 12), mainGate: make(chan struct{}), sideGate: make(chan struct{}), cancelGate: make(chan struct{}), canceled: make(chan struct{})}
	defer func() {
		for _, gate := range []chan struct{}{p.mainGate, p.sideGate, p.cancelGate} {
			select {
			case <-gate:
			default:
				close(gate)
			}
		}
	}()
	proxy := newManagedMekugiProxy(t)
	agentProvider := &agentQuestionCodexProvider{btwCodexProvider: p, gate: make(chan struct{}), done: make(chan error, 1)}
	server := httptest.NewServer(inferencegateway.ResponsesHandler(t.Context(), time.Minute, testRequestExecutor(agentProvider, nil, proxy).ExecuteHTTP))
	defer server.Close()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	integration := testAppServerIntegration(proxy)
	terminal := startAppResumeTerminalWithIntegration(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "-c", `model_providers.preview={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.multi_agent_v2.enabled=true", "-c", "features.multi_agent_v2.disable_native_tools=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "", integration)
	next := func() btwCodexRequest {
		t.Helper()
		select {
		case request := <-p.requests:
			return request
		case <-terminal.ctx.Done():
			t.Fatal(terminal.ctx.Err())
			return btwCodexRequest{}
		}
	}
	terminal.await("Ready")
	terminal.send("Main seed\r")
	next()
	terminal.await("completed")
	terminal.send("Main running\r")
	next()
	terminal.await("Working")
	terminal.send("/btw Side question\r")
	human := next()
	terminal.await("Side streaming answer")
	close(agentProvider.gate)
	agent := next()
	if !bytes.Contains(agent.body, []byte("Main seed")) || !bytes.Contains(agent.body, []byte("Main running")) || bytes.Contains(agent.body, []byte("Side streaming answer")) {
		t.Fatal("agent question captured the wrong primary context")
	}
	agentMetadata, _ := codexresponses.DecodeTurnMetadata(agent.headers)
	humanMetadata, _ := codexresponses.DecodeTurnMetadata(human.headers)
	if agentMetadata.ThreadID == humanMetadata.ThreadID {
		t.Fatal("agent question occupied the human BTW slot")
	}
	select {
	case err := <-agentProvider.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-terminal.ctx.Done():
		t.Fatal(terminal.ctx.Err())
	}
	close(p.sideGate)
	terminal.await("/btw · completed")
	terminal.send("\x1b")
	close(p.mainGate)
	terminal.await("completed")
	terminal.send("Main after question\r")
	after := next()
	if bytes.Contains(after.body, []byte("AGENT-QUESTION")) || bytes.Contains(after.body, []byte("Side followup answer")) {
		t.Fatal("question polluted target primary history")
	}
	terminal.await("Main after question")
	terminal.await("completed")
	terminal.quit()
}

// The worker asks through its normal functions.exec tool, while the parent and
// human BTW requests are independently held open by the consuming test.
type agentQuestionCodexProvider struct {
	*btwCodexProvider
	mu                    sync.Mutex
	root                  string
	rootCalls, childCalls int
	gate                  chan struct{}
	done                  chan error
}

func (p *agentQuestionCodexProvider) ForwardExecution(ctx, responseCtx context.Context, body []byte, headers http.Header, key string) (*inferencegateway.Response, error) {
	prompt := btwLastUserPrompt(body)
	// Side answers retain the parent's context, so classify by their actual new
	// user question before looking for the copied worker assignment.
	if strings.Contains(prompt, "Side question") || strings.Contains(prompt, "Side followup") {
		return p.btwCodexProvider.ForwardExecution(ctx, responseCtx, body, headers, key)
	}
	metadata, _ := codexresponses.DecodeTurnMetadata(headers)
	p.mu.Lock()
	if p.root == "" {
		p.root = metadata.ThreadID
	}
	main := metadata.ThreadID == p.root
	if main {
		p.rootCalls++
	} else {
		p.childCalls++
	}
	step := p.rootCalls
	if !main {
		step = p.childCalls
	}
	p.mu.Unlock()
	if main && step == 2 {
		return nativeCollabResponse(`text(await tools.collaboration__spawn_agent({task_name:"worker",message:"WORKER_QUESTION",fork_turns:"none"}));`, "")
	}
	if !main {
		if step == 1 {
			select {
			case <-p.gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return nativeCollabResponse(`text(await tools.collaboration__send_message({target:"/root",btw:true,message:"Side followup AGENT-QUESTION"}));`, "")
		}
		var err error
		if !bytes.Contains(body, []byte("Side followup answer")) {
			err = fmt.Errorf("native model question omitted the returned answer")
		}
		p.done <- err
		return nativeCollabResponse("", "Worker question complete.")
	}
	return p.btwCodexProvider.ForwardExecution(ctx, responseCtx, body, headers, key)
}

func btwLastUserPrompt(body []byte) string {
	var request struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil {
		return ""
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
	return prompt
}

type btwCodexProvider struct {
	requests                       chan btwCodexRequest
	mainGate, sideGate, cancelGate chan struct{}
	canceled                       chan struct{}
}

func (p *btwCodexProvider) ForwardExecution(ctx, responseCtx context.Context, body []byte, headers http.Header, key string) (*inferencegateway.Response, error) {
	p.requests <- btwCodexRequest{bytes.Clone(body), key, headers.Clone()}
	prompt := btwLastUserPrompt(body)
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
		return testGatewayResponse(response, nil)
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
	return testGatewayResponse(response, nil)
}

func TestAppServerBTWNativeCodexE2E(t *testing.T) {
	for _, scenario := range []string{"idle", "active", "resumed"} {
		t.Run(scenario, func(t *testing.T) {
			active := scenario == "active"
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			codex, err := exec.LookPath("mekugi-app-server")
			if err != nil {
				t.Fatal(err)
			}
			p := &btwCodexProvider{requests: make(chan btwCodexRequest, 12), mainGate: make(chan struct{}), sideGate: make(chan struct{}), cancelGate: make(chan struct{}), canceled: make(chan struct{})}
			proxy := newManagedMekugiProxy(t)
			catalog := new(inferencegateway.ReasoningCatalog)
			catalog.Capture([]byte(`{"models":[{"slug":"gpt-6-sol","default_reasoning_level":"high","supports_reasoning_effort_updates":true,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},{"slug":"gpt-6-astra","default_reasoning_level":"high","supports_reasoning_effort_updates":true,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`))
			executor := testRequestExecutor(p, nil, proxy)
			executor.ReasoningChoices = catalog.Choices
			server := httptest.NewServer(inferencegateway.ResponsesHandler(t.Context(), time.Minute, executor.ExecuteHTTP))
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
				cmd := exec.CommandContext(ctx, codex, "-c", `model_providers.preview={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.multi_agent_v2.enabled=true", "-c", "features.multi_agent_v2.disable_native_tools=false")
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}
			terminal := startAppResumeTerminalWithIntegration(t, command, "", testAppServerIntegration(proxy))
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
				metadata, _ := codexresponses.DecodeTurnMetadata(main.headers)
				terminal.stopCanceled()
				terminal = startAppResumeTerminalWithIntegration(t, command, metadata.ThreadID, testAppServerIntegration(proxy))
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
			mainMetadata, _ := codexresponses.DecodeTurnMetadata(main.headers)
			sideMetadata, _ := codexresponses.DecodeTurnMetadata(side.headers)
			if side.key == "" || main.key == "" || sideMetadata.ThreadID == "" || sideMetadata.ThreadID == mainMetadata.ThreadID {
				t.Fatalf("fork identities: main=%q side=%q", mainMetadata.ThreadID, sideMetadata.ThreadID)
			}
			var mainSettings, sideSettings struct {
				Model string `json:"model"`
				Input []struct {
					Type      string `json:"type"`
					Reasoning struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
				} `json:"input"`
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
			if mainSettings.Model != sideSettings.Model || mainSettings.Reasoning.Effort != "high" || sideSettings.Reasoning.Effort != "high" {
				t.Fatalf("side reasoning isolation: main=%+v side=%+v", mainSettings, sideSettings)
			}
			effective := sideSettings.Reasoning.Effort
			for _, item := range sideSettings.Input {
				if item.Type == "configuration_update" {
					effective = item.Reasoning.Effort
				}
			}
			if effective != "low" {
				t.Fatalf("BTW effective effort = %q", effective)
			}
			if !bytes.Contains(side.body, []byte("Main seed")) || active && !bytes.Contains(side.body, []byte("Main running")) {
				t.Fatal("branch omitted the current conversation")
			}
			terminal.await("Side streaming answer")
			if active {
				terminal.await("Working")
			}
			close(p.sideGate)
			terminal.awaitMatch("side completed", func(frame string) bool { return strings.Contains(frame, "/btw · completed") })
			terminal.send("/btw Side followup\r")
			followup := next()
			if followup.key != side.key || !bytes.Contains(followup.body, []byte("Side question")) || !bytes.Contains(followup.body, []byte("Side streaming answer")) {
				t.Fatal("follow-up did not retain its side context and identity")
			}
			terminal.await("Side followup answer")
			terminal.awaitMatch("side completed", func(frame string) bool { return strings.Contains(frame, "/btw · completed") })
			if active {
				terminal.send("/btw Side cancel\r")
				next()
				terminal.await("Side cancel streaming")
			}
			terminal.send("\x021kept draft\x1b")
			terminal.awaitMatch("side closed, Main draft kept", func(frame string) bool {
				return !strings.Contains(frame, "/btw ·") && strings.Contains(frame, "kept draft")
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
