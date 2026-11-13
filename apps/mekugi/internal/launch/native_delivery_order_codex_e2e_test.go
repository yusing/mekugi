//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

const nativeDeliveryBody = "Delivered before reasoning: keep these exact bytes."

// These fixtures drive actual model-authored native tools, not a Go input bridge.
func nativeCollabResponse(program, answer string) (*inferencegateway.Response, error) {
	id := "native-collab-response"
	item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	if program != "" {
		item = map[string]any{"type": "custom_tool_call", "id": id, "call_id": id + "-call", "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
	}
	wire := routerFaultSSE(
		map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
		map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}}},
	)
	return testGatewayResponse(&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil)
}

// Withhold even the reasoning item's creation until the real worker's message
// is painted. A later delta must not fill an item ordered before injection.
type deliveryOrderProvider struct {
	appReasoningStreamProvider
	mu                      sync.Mutex
	root                    string
	calls                   map[string]int
	mainReady, acknowledged chan struct{}
	retained                chan bool
}

func (p *deliveryOrderProvider) ForwardExecution(parent, ctx context.Context, body []byte, headers http.Header, key string) (*inferencegateway.Response, error) {
	metadata, _ := codexresponses.DecodeTurnMetadata(headers)
	thread := headers.Get(codexresponses.ThreadIDHeader)
	if thread == "" {
		thread = metadata.ThreadID
	}
	p.mu.Lock()
	if p.root == "" {
		p.root = thread
	}
	p.calls[thread]++
	step, main := p.calls[thread], thread == p.root
	p.mu.Unlock()
	if main && step == 1 {
		return nativeCollabResponse(`text(await tools.collaboration__spawn_agent({task_name:"worker",message:"Send the attributed delivery fixture message, then finish.",fork_turns:"none"}));`, "")
	}
	if !main {
		if step == 1 {
			select {
			case <-p.mainReady:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return nativeCollabResponse(`await tools.collaboration__send_message({target:"/root",message:`+strconv.Quote(nativeDeliveryBody)+`});`, "")
		}
		close(p.acknowledged)
		return nativeCollabResponse("", "Worker delivery complete.")
	}
	if step == 2 {
		close(p.mainReady)
		select {
		case <-p.stages[0]:
			return p.appReasoningStreamProvider.ForwardExecution(parent, ctx, body, headers, key)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.retained <- bytes.Count(body, []byte(nativeDeliveryBody)) == 1
	return nativeCollabResponse("", "Retained delivery confirmed.")
}

// This covers the real runtime injection-to-terminal boundary. Selected waits
// remain covered separately by TestNativeMessageVisibleBeforeWaitContinuation.
func TestNativeMessageBeforeReasoningCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("mekugi-app-server")
	if err != nil {
		t.Fatal(err)
	}
	p := &deliveryOrderProvider{
		appReasoningStreamProvider: appReasoningStreamProvider{stages: [3]chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}},
		calls:                      make(map[string]int), mainReady: make(chan struct{}), acknowledged: make(chan struct{}), retained: make(chan bool, 1),
	}
	proxy := newManagedMekugiProxy(t)
	server := httptest.NewServer(inferencegateway.ResponsesHandler(t.Context(), time.Minute, testRequestExecutor(p, nil, proxy).ExecuteHTTP))
	defer server.Close()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	terminal := startAppResumeTerminalWithIntegration(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.multi_agent_v2.enabled=true", "-c", "features.multi_agent_v2.disable_native_tools=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "", testAppServerIntegration(proxy))
	defer terminal.cancel()
	// PTY reads may split or combine writes. Inspect every complete synchronized
	// frame instead of inferring ordering from accumulated final output.
	var pending synchronizedFrameBuffer
	awaitFrame := func(needle string) string {
		t.Helper()
		if visible := terminal.screen.String(); strings.Contains(visible, needle) {
			return visible
		}
		for {
			if frame, ok := pending.Next(); ok {
				terminal.screen.Write(frame)
				if visible := terminal.screen.String(); strings.Contains(visible, needle) {
					return visible
				}
				continue
			}
			select {
			case chunk, ok := <-terminal.frames:
				if !ok {
					t.Fatalf("terminal closed before %q\n%s", needle, terminal.screen.String())
				}
				pending.Append(chunk)
			case err := <-terminal.done:
				t.Fatalf("terminal exited before %q: %v\n%s", needle, err, terminal.screen.String())
			case <-terminal.ctx.Done():
				t.Fatalf("missing frame with %q: %v\n%s", needle, terminal.ctx.Err(), terminal.screen.String())
			}
		}
	}
	awaitFrame("Ready")
	terminal.send("Exercise attributed message delivery.\r")
	select {
	case <-p.acknowledged:
	case <-terminal.ctx.Done():
		t.Fatal("native message tool did not settle:", terminal.ctx.Err())
	}
	acknowledged := time.Now()
	const body = nativeDeliveryBody
	frame := awaitFrame(body)
	if !strings.Contains(frame, "✉ worker → main ") || strings.Contains(frame, "public checkpoint") {
		t.Fatalf("first message frame lost attribution or required reasoning:\n%s", frame)
	}
	t.Logf("message frame observed %s after successful injection acknowledgement, before any provider response", time.Since(acknowledged))
	close(p.stages[0])
	for i, checkpoint := range []string{"First public checkpoint.", "Second public checkpoint."} {
		if i == 1 {
			close(p.stages[1])
		}
		frame = awaitFrame(checkpoint)
		if message := strings.Index(frame, body); message < 0 || message >= strings.Index(frame, checkpoint) || strings.Count(frame, body) != 1 {
			t.Fatalf("message did not precede %q exactly once:\n%s", checkpoint, frame)
		}
	}
	close(p.stages[2])
	awaitFrame("╭─ Completed")
	// Confirm exact bytes through the next actual provider request, after the
	// native recipient consumed the message at its inference boundary.
	terminal.send("Confirm retained delivery.\r")
	select {
	case exact := <-p.retained:
		if !exact {
			t.Fatal("native history lost or duplicated attributed input")
		}
	case <-terminal.ctx.Done():
		t.Fatal(terminal.ctx.Err())
	}
	awaitFrame("Retained delivery confirmed.")
	awaitFrame("╭─ Completed")
	terminal.quit()
}
