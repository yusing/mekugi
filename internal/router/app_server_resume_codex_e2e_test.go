//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/capturer"
	"golang.org/x/term"
)

type appResumeProvider struct {
	mu       sync.Mutex
	threads  []string
	settings []appResumeInferenceSettings
}

type appResumeInferenceSettings struct {
	Model     string `json:"model"`
	Tier      string `json:"service_tier"`
	Reasoning struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
}

func (p *appResumeProvider) forwardExecution(_, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	var settings appResumeInferenceSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		return nil, err
	}
	thread := headers.Get(threadIDHeader)
	if thread == "" {
		metadata, _ := decodeCodexTurnMetadata(headers)
		thread = metadata.ThreadID
	}
	p.mu.Lock()
	p.threads = append(p.threads, thread)
	p.settings = append(p.settings, settings)
	p.mu.Unlock()
	return routerFaultCodexSuccessResponse(), nil
}

func (p *appResumeProvider) assertSettings(t *testing.T, index int, model, effort, tier string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if index >= len(p.settings) {
		t.Fatalf("missing inference %d: %d requests", index, len(p.settings))
	}
	got := p.settings[index]
	if got.Model != model || got.Reasoning.Effort != effort || got.Tier != tier {
		t.Fatalf("inference %d settings: %+v, want %s/%s/%s", index, got, model, effort, tier)
	}
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
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
	defer server.Close()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // State belongs to the parent UI, not just its Codex child.
	environment := routerFaultCodexEnvironment(t)
	workspace := t.TempDir()
	model := "gpt-6-astra"
	var extraOverrides []string
	providerName := "savedpreview"
	newCommand := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server",
			"-c", `model_providers.`+providerName+`={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", "model_provider="+strconv.Quote(providerName),
			"-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		if model != "" {
			cmd.Args = append(cmd.Args, "-c", "model="+strconv.Quote(model))
		}
		cmd.Args = append(cmd.Args, extraOverrides...)
		cmd.Env = environment
		cmd.Dir = workspace
		return cmd
	}

	first := startAppResumeTerminal(t, newCommand, "")
	first.await("Ready")
	first.send("/model gpt-6-sol\r")
	first.await("gpt-6-sol")
	first.send("/reasoning high\r")
	first.await("gpt-6-sol (high)")
	first.send("/tier flex\r")
	first.await("gpt-6-sol (high) · flex")
	first.send("First resume question\r")
	first.await("Recovered after a retry.")
	first.await("completed")
	first.send("/tier priority\r") // Idle changes after the last turn must also restore.
	first.await("Settings saved")
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
	provider.assertSettings(t, 0, "gpt-6-sol", "high", "flex")
	foundSaved := false
	for _, variable := range environment {
		if home, ok := strings.CutPrefix(variable, "CODEX_HOME="); ok {
			err := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
					return nil
				}
				if saved := readResumeSettings(appServerThreadInfo{ID: thread, Path: path}, time.Time{}); saved["model"] != nil {
					foundSaved = true
					if saved["model"] != "gpt-6-sol" || saved["model_reasoning_effort"] != "high" || saved["service_tier"] != "priority" {
						t.Fatalf("host persisted settings: %#v", saved)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !foundSaved {
		t.Fatal("host did not retain the completed thread's settings")
	}

	model, providerName = "", "preview"
	second := startAppResumeTerminal(t, newCommand, "--last")
	second.await("Ready")
	second.await("gpt-6-sol (high)") // Retained usage can crowd the tier out of the narrow footer.
	second.await("First resume question")
	second.await("Recovered after a retry.")
	second.awaitMatch("restored Diff focus and split", func(screen string) bool {
		return strings.Contains(screen, "2 Diff") && strings.Contains(screen, "s files") && appResumeSplit(screen) == savedSplit
	})
	if got := provider.snapshot(); len(got) != 1 {
		t.Fatalf("resume resent a turn before new input: %q", got)
	}
	second.send("\x021")
	second.send("Second resume question\r")
	second.await("Second resume question")
	second.await("completed")
	second.quit()
	threads = provider.snapshot()
	provider.assertSettings(t, 1, "gpt-6-sol", "high", "priority")
	if len(threads) != 2 || threads[1] != thread {
		t.Fatalf("turns did not retain one thread with no duplicate submission: %q", threads)
	}
	model = "gpt-6-astra"
	third := startAppResumeTerminal(t, newCommand, thread)
	third.await("Ready")
	third.await("gpt-6-astra (high)")
	third.send("Explicit model keeps saved effort and tier\r")
	third.await("completed")
	third.quit()
	provider.assertSettings(t, 2, "gpt-6-astra", "high", "priority")
	model = ""
	extraOverrides = []string{"-c", `model_reasoning_effort="low"`, "-c", `service_tier="flex"`}
	fourth := startAppResumeTerminal(t, newCommand, thread)
	fourth.await("Ready")
	fourth.await("gpt-6-astra (low)")
	fourth.send("Explicit effort and tier keep saved model\r")
	fourth.await("completed")
	fourth.quit()
	provider.assertSettings(t, 3, "gpt-6-astra", "low", "flex")
	if got := provider.snapshot(); len(got) != 4 {
		t.Fatalf("resume resent an unrequested turn: %q", got)
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

func startAppResumeTerminalWithProxy(t *testing.T, newCommand func(context.Context) *exec.Cmd, resumeThread string, proxy *mekugiProxy, capture ...*capturer.Recorder) *appResumeTerminal {
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
	var recorder *capturer.Recorder
	if len(capture) > 0 {
		recorder = capture[0]
	}
	wait, err := startAppServerUI(ctx, newCommand(ctx), inner, inner, proxy, nil, resumeThread, true, nil, recorder, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	s := &appResumeTerminal{t: t, ctx: ctx, cancel: cancel, outer: outer, inner: inner,
		frames: make(chan []byte, 32), done: make(chan error, 1), screen: vt.NewEmulator(100, 30), before: before}
	t.Cleanup(func() { s.screen.Close() })
	// Answer terminal capability/color queries so emulator writes cannot block
	// on its response pipe before the first frame reaches the assertion.
	go func() { _, _ = io.Copy(outer, s.screen) }()
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

func TestAppServerOptionWordNavigationNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	environment := routerFaultCodexEnvironment(t)
	workspace := t.TempDir()
	provider := &appResumeProvider{}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
	defer server.Close()
	terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server",
			"-c", `model_providers.keys={name="keys",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="keys"`, "-c", `model="gpt-6-astra"`)
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "")
	terminal.await("Ready")
	terminal.send("one two three\x1bb\x1bbX\x1bfY")
	terminal.await("one Xtwo Ythree")
	terminal.send("\x03")
	terminal.send("alpha beta gamma\x1b[1;3D\x1b[1;3DX\x1b[1;3CY")
	terminal.await("alpha Xbeta Ygamma")
	terminal.send("\x03")
	terminal.quit()
	if got := provider.snapshot(); len(got) != 0 {
		t.Fatalf("word navigation submitted a model request: %q", got)
	}
}

// The picker lists installed Codex's saved sessions, switches the running UI
// to one, and serves a bare startup resume. Provider thread identities prove
// where later input went.
func TestAppServerResumePickerNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	provider := &appResumeProvider{}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
	defer server.Close()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	environment := routerFaultCodexEnvironment(t)
	workspace := t.TempDir()
	newCommand := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server",
			"-c", `model_providers.pickerpreview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="pickerpreview"`, "-c", `model="gpt-6-astra"`,
			"-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env = environment
		cmd.Dir = workspace
		return cmd
	}

	first := startAppResumeTerminal(t, newCommand, "")
	first.await("Ready")
	first.send("Picker first question\r")
	first.await("completed")
	first.quit()

	second := startAppResumeTerminal(t, newCommand, "")
	second.await("Ready")
	second.send("Picker second question\r")
	second.await("completed")
	second.send("/resume\r")
	second.await("Resume a previous session")
	second.awaitMatch("both saved sessions", func(screen string) bool {
		return strings.Contains(screen, "Picker first question") && strings.Contains(screen, "Picker second question · current")
	})
	second.send("\x1b[B\r")
	second.awaitMatch("switched transcript", func(screen string) bool {
		return strings.Contains(screen, "Picker first question") && !strings.Contains(screen, "Picker second question") && !strings.Contains(screen, "Resume a previous session")
	})
	second.await("Ready")
	second.send("Picker follow-up\r")
	second.await("Picker follow-up")
	second.await("completed")
	second.quit()
	threads := provider.snapshot()
	if len(threads) != 3 || threads[0] == threads[1] || threads[2] != threads[0] {
		t.Fatalf("switched input did not reach the chosen thread: %q", threads)
	}

	third := startAppResumeTerminal(t, newCommand, resumePickerStartup)
	third.await("Resume a previous session")
	third.await("esc start new")
	// The follow-up made the first session the most recently updated.
	third.awaitMatch("recent session first", func(screen string) bool {
		first, second := strings.Index(screen, "Picker first question"), strings.Index(screen, "Picker second question")
		return first >= 0 && second > first
	})
	third.send("\r")
	third.await("Picker follow-up")
	third.await("Ready")
	third.quit()
	if got := provider.snapshot(); len(got) != 3 {
		t.Fatalf("startup picker resent a turn: %q", got)
	}
}
