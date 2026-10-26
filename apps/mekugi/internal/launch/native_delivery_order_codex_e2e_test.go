//go:build codex_e2e

package launch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Withhold even the reasoning item's creation until the message is painted.
// Otherwise later text could fill an item that legitimately precedes injection.
type deliveryOrderProvider struct {
	appReasoningStreamProvider
	thread chan string
}

func (p *deliveryOrderProvider) ForwardExecution(parent, ctx context.Context, body []byte, headers http.Header, key string) (*inferencegateway.Response, error) {
	thread := headers.Get(codexresponses.ThreadIDHeader)
	if thread == "" {
		metadata, _ := codexresponses.DecodeTurnMetadata(headers)
		thread = metadata.ThreadID
	}
	select {
	case p.thread <- thread:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.stages[0]:
		return p.appReasoningStreamProvider.ForwardExecution(parent, ctx, body, headers, key)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// This covers the real runtime injection-to-terminal boundary. Selected waits
// remain covered separately by TestNativeMessageVisibleBeforeWaitContinuation.
func TestNativeMessageBeforeReasoningCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	p := &deliveryOrderProvider{
		appReasoningStreamProvider: appReasoningStreamProvider{stages: [3]chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}},
		thread:                     make(chan string, 1),
	}
	server := httptest.NewServer(testResponsesHandler(t.Context(), time.Minute, p, nil, nil))
	defer server.Close()
	hubs := make(chan *sessionapi.Hub, 1)
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	terminal := startAppResumeTerminalWithIntegration(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "", appServerIntegration{nativeHub: func(hub *sessionapi.Hub) { hubs <- hub }})
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
	hub := <-hubs // Startup published the Hub before admitting the ready runtime.
	terminal.send("Exercise attributed message delivery.\r")
	var thread string
	select {
	case thread = <-p.thread:
	case <-terminal.ctx.Done():
		t.Fatal("provider did not receive the initial request:", terminal.ctx.Err())
	}
	runtime, err := hub.Requests(thread)
	if err != nil {
		t.Fatal(err)
	}
	const body = "Delivered before reasoning: keep these exact bytes."
	input := control.NativeInput{Thread: thread, ID: "terminal-message-order", Sender: "/root/worker", Recipient: "/root", Kind: "MESSAGE", Message: body}
	if _, err := runtime.Input(terminal.ctx, input); err != nil {
		t.Fatal("installed host injection acknowledgement:", err)
	}
	acknowledged := time.Now()
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
	// A running host queues injected items until its inference boundary. Verify
	// durable exact bytes after settlement, separately from immediate painting.
	if err := runtime.Confirm(terminal.ctx, input); err != nil {
		t.Fatal("settled host did not retain the exact attributed input:", err)
	}
	terminal.quit()
}
