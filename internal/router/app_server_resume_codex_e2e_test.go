//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/term"
)

type appResumeProvider struct {
	mu      sync.Mutex
	threads []string
}

func (p *appResumeProvider) forwardExecution(_, _ context.Context, _ []byte, headers http.Header, _ string) (*http.Response, error) {
	thread := headers.Get(threadIDHeader)
	if thread == "" {
		metadata, _ := decodeCodexTurnMetadata(headers)
		thread = metadata.ThreadID
	}
	p.mu.Lock()
	p.threads = append(p.threads, thread)
	p.mu.Unlock()
	return routerFaultCodexSuccessResponse(), nil
}

func (p *appResumeProvider) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.threads...)
}

// Runs fresh installed Codex processes against one isolated Codex home.
// The provider is local and deterministic; no account or live model is used.
func TestAppServerResumeNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	provider := &appResumeProvider{}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil))
	defer server.Close()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // State belongs to the parent UI, not just its Codex child.
	environment := routerFaultCodexEnvironment(t)
	workspace := t.TempDir()
	model := "gpt-6-astra"
	providerName := "savedpreview"
	newCommand := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server",
			"-c", `model_providers.`+providerName+`={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", "model_provider="+strconv.Quote(providerName),
			"-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		if model != "" {
			cmd.Args = append(cmd.Args, "-c", "model="+strconv.Quote(model))
		}
		cmd.Env = environment
		cmd.Dir = workspace
		return cmd
	}

	first := startAppResumeTerminal(t, newCommand, "")
	first.await("Ready")
	first.send("First resume question\r")
	first.await("Recovered after a retry.")
	first.await("completed")
	first.send("\x022") // Focus Diff, then widen Main before persisting this UI state.
	first.await("s files")
	first.send("\x02l")
	first.awaitMatch("resized split", func(screen string) bool {
		return appResumeSplit(screen) > 50 && strings.Contains(screen, "s files")
	})
	savedSplit := appResumeSplit(first.screen.String())
	first.stopCanceled()
	threads := provider.snapshot()
	if len(threads) != 1 || threads[0] == "" {
		t.Fatalf("first turn identities: %q", threads)
	}
	thread := threads[0]

	model, providerName = "", "preview"
	second := startAppResumeTerminal(t, newCommand, thread)
	second.await("Ready")
	second.await("First resume question")
	second.await("Recovered after a retry.")
	second.awaitMatch("restored Diff focus and split", func(screen string) bool {
		return strings.Contains(screen, "2 Diff") && strings.Contains(screen, "s files") && appResumeSplit(screen) == savedSplit
	})
	if got := provider.snapshot(); len(got) != 1 {
		t.Fatalf("resume resent a turn before new input: %q", got)
	}
	second.send("\x021")
	second.await("⏎ send")
	second.send("Second resume question\r")
	second.await("Second resume question")
	second.await("completed")
	second.quit()
	threads = provider.snapshot()
	if len(threads) != 2 || threads[1] != thread {
		t.Fatalf("turns did not retain one thread with no duplicate submission: %q", threads)
	}
	model = "gpt-6-sol"
	third := startAppResumeTerminal(t, newCommand, thread)
	third.await("Ready")
	third.await("gpt-6-sol")
	third.quit()
	if got := provider.snapshot(); len(got) != 2 {
		t.Fatalf("model override resume resent a turn: %q", got)
	}
}

// The second top-border corner is the right pane's origin in a 100-column PTY.
func appResumeSplit(screen string) int {
	row, _, _ := strings.Cut(screen, "\n")
	for i, r := range []rune(row) {
		if r == '┌' && i > 0 {
			return i
		}
	}
	return -1
}

type appResumeTerminal struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc
	outer  *os.File
	inner  *os.File
	frames chan []byte
	done   chan error
	screen *vt.Emulator
	before *term.State
}

func startAppResumeTerminal(t *testing.T, newCommand func(context.Context) *exec.Cmd, resumeThread string) *appResumeTerminal {
	return startAppResumeTerminalWithProxy(t, newCommand, resumeThread, nil)
}

func startAppResumeTerminalWithProxy(t *testing.T, newCommand func(context.Context) *exec.Cmd, resumeThread string, proxy *mekugiProxy) *appResumeTerminal {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	outer, inner, err := pty.Open()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); outer.Close(); inner.Close() })
	if err := pty.Setsize(outer, &pty.Winsize{Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(inner.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	wait, err := startAppServerUI(ctx, newCommand(ctx), inner, inner, proxy, resumeThread)
	if err != nil {
		t.Fatal(err)
	}
	s := &appResumeTerminal{t: t, ctx: ctx, cancel: cancel, outer: outer, inner: inner,
		frames: make(chan []byte, 32), done: make(chan error, 1), screen: vt.NewEmulator(100, 30), before: before}
	t.Cleanup(func() { s.screen.Close() })
	go func() { s.done <- wait() }()
	go func() {
		defer close(s.frames)
		buf := make([]byte, 65536)
		for {
			n, err := outer.Read(buf)
			if n > 0 {
				select {
				case s.frames <- bytes.Clone(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *appResumeTerminal) await(needle string) {
	s.t.Helper()
	s.awaitMatch(needle, func(content string) bool {
		if needle == "completed" {
			return strings.Contains(content, "╭─ Completed")
		}
		return strings.Contains(content, needle)
	})
}

func (s *appResumeTerminal) awaitMatch(label string, visible func(string) bool) {
	s.t.Helper()
	for !visible(s.screen.String()) {
		select {
		case frame, ok := <-s.frames:
			if !ok {
				s.t.Fatalf("terminal closed before %q\n%s", label, s.screen.String())
			}
			s.screen.Write(frame)
		case err := <-s.done:
			s.t.Fatalf("UI exited before %q: %v\n%s", label, err, s.screen.String())
		case <-s.ctx.Done():
			s.t.Fatalf("missing %q: %v\n%s", label, s.ctx.Err(), s.screen.String())
		}
	}
}

func (s *appResumeTerminal) stopCanceled() {
	s.t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		if !errors.Is(err, context.Canceled) {
			s.t.Fatalf("canceled UI returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		s.t.Fatal("canceled UI did not shut down")
	}
	s.checkRestored()
}

func (s *appResumeTerminal) send(input string) {
	s.t.Helper()
	if _, err := io.WriteString(s.outer, input); err != nil {
		s.t.Fatal(err)
	}
}

func (s *appResumeTerminal) quit() {
	s.t.Helper()
	s.send("/quit\r")
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Fatal(err)
		}
	case <-s.ctx.Done():
		s.t.Fatal(s.ctx.Err())
	}
	s.checkRestored()
}

func (s *appResumeTerminal) checkRestored() {
	s.t.Helper()
	after, err := term.GetState(int(s.inner.Fd()))
	if err != nil {
		s.t.Fatal(err)
	}
	if !reflect.DeepEqual(s.before, after) {
		s.t.Fatal("terminal mode not restored")
	}
	s.cancel()
	s.outer.Close()
	s.inner.Close()
}
