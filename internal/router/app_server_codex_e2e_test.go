//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/term"
)

type appPreviewProvider struct{}

func (*appPreviewProvider) forwardExecution(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
	return routerFaultCodexSuccessResponse(), nil
}

// Real Codex, real stdio and terminal rendering, deterministic local provider.
// No credentials or live model calls. This is not the full migration gate.
func TestAppServerPreviewNativeCodex(t *testing.T) {
	runAppServerPreview(t, &appPreviewProvider{})
}

func TestAppServerPreviewCodeMode(t *testing.T) {
	provider := &toolFrontendCodexProvider{
		program:   `const result = await tools.exec_command({cmd:"printf APP_SERVER_TOOL_OK"}); text(result.output);`,
		expected:  []string{"APP_SERVER_TOOL_OK"},
		finalText: "Recovered after a retry.",
	}
	runAppServerPreview(t, provider)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.turns != 2 || !provider.resultSeen {
		t.Fatalf("Code Mode did not complete exactly once: turns=%d result=%v", provider.turns, provider.resultSeen)
	}
}

func runAppServerPreview(t *testing.T, provider responseProvider) {
	runAppServerPreviewWithProxy(t, provider, nil)
}

func TestAppServerPreviewNativeJournal(t *testing.T) {
	runAppServerPreviewWithProxy(t, &appPreviewProvider{}, newManagedMekugiProxy(t))
}

func runAppServerPreviewWithProxy(t *testing.T, provider responseProvider, proxy *mekugiProxy) {
	t.Helper()
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy, nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env = routerFaultCodexEnvironment(t)
	cmd.Dir = t.TempDir()
	outer, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	defer terminal.Close()
	if err := pty.Setsize(outer, &pty.Winsize{Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	wait, err := startAppServerUI(ctx, cmd, terminal, terminal, proxy)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	frames := make(chan []byte, 32)
	go func() {
		defer close(frames)
		buf := make([]byte, 65536)
		for {
			n, err := outer.Read(buf)
			if n > 0 {
				select {
				case frames <- bytes.Clone(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	screen := vt.NewEmulator(100, 30)
	defer screen.Close()
	await := func(needle string) {
		t.Helper()
		visible := func() bool {
			content := screen.String()
			if needle == "completed" {
				content = strings.SplitN(content, "\n", 2)[0]
			}
			return strings.Contains(content, needle)
		}
		for !visible() {
			select {
			case p, ok := <-frames:
				if !ok {
					t.Fatal("terminal closed")
				}
				screen.Write(p)
			case err := <-done:
				t.Fatalf("UI exited: %v\n%s", err, screen.String())
			case <-ctx.Done():
				t.Fatalf("missing %q\n%s", needle, screen.String())
			}
		}
	}
	await("Ready")
	await("AGENTS")
	var native *nativeJournalSink
	if proxy != nil {
		proxy.journals.nativeMu.Lock()
		for _, sink := range proxy.journals.native {
			if sink.workspace == "" {
				native = sink
			}
		}
		proxy.journals.nativeMu.Unlock()
		if native == nil {
			t.Fatal("native sink not attached before first prompt")
		}
		if err := proxy.journals.initialize(ctx, proxy.replayStore, native.workspace, native.thread, "/root", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.journals.apply(ctx, proxy.replayStore, native.workspace, native.thread, "live", []journalMutation{{Op: "add", Text: new("Native milestone"), ReportNow: true}}); err != nil {
			t.Fatal(err)
		}
		await("Native milestone")
	}
	io.WriteString(outer, "A deterministic preview prompt\r")
	await("Recovered after a retry.")
	await("completed")
	if strings.Contains(screen.String(), "Journal flush") || strings.Contains(screen.String(), "Question:") {

		t.Fatalf("legacy flush carrier leaked into Main:\n%s", screen.String())
	}
	if !strings.Contains(screen.String(), "A deterministic preview prompt") {
		t.Fatal("user message missing from shared view")
	}
	t.Logf("Rendered terminal:\n%s", screen.String())
	io.WriteString(outer, "/quit\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("terminal mode not restored")
	}
	if native != nil {
		items, err := proxy.journals.list(ctx, proxy.replayStore, native.workspace, native.thread)
		if err != nil || len(items) != 2 {
			t.Fatalf("native journal items: %+v %v", items, err)
		}
		for _, item := range items {
			if !item.Reported || !item.Flushed {
				t.Fatalf("rendered revision missing receipt: %+v", item)
			}
		}
	}
}
