//go:build unix

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Two real prompts, with normal native billing/configuration. No synthetic events,
// provider proxy, permission-mode override, or delayed native tool completion.
// Each launch runs the shared UI in a separate OS process, not just a new bridge.
func TestNativeRuntimeClaudePTYLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("set MEKUGI_TEST_NATIVE_CLAUDE=1 for real Claude PTY acceptance")
	}
	nativeAcceptanceSettingsUnchanged(t)
	version, err := exec.Command("claude", "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "2.1.287 (Claude Code)" {
		t.Fatal("native acceptance requires installed Claude 2.1.287")
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal("run make test-claude before native PTY acceptance")
	}
	packageData, err := os.ReadFile("../claude/bridge/node_modules/@anthropic-ai/claude-agent-sdk/package.json")
	var sdk struct {
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(packageData, &sdk) != nil || sdk.Version != "0.3.287" {
		t.Fatal("native acceptance requires built SDK 0.3.287")
	}
	workspace, state := t.TempDir(), t.TempDir()
	var content strings.Builder
	for i := range 100 {
		fmt.Fprintf(&content, "NATIVE_PTY_ROW_%03d amber cedar violet\n", i)
	}
	path := filepath.Join(workspace, "native-runtime-claude-pty.txt")
	first := startNativeClaudePTY(t, workspace, state, bridge, "", "first")
	first.await("ready", func(s string) bool { return strings.Contains(s, "Ready") })
	first.keys("\x02" + "2")
	first.await("Diff focus", func(s string) bool { return strings.Contains(s, "2 Diff") })
	prompt := fmt.Sprintf("Remember the private conversation marker NATIVE_PTY_MEMORY_CEDAR. Use your native Write tool exactly once to create %s with exactly 100 lines, numbered 000 through 099, each in the format NATIVE_PTY_ROW_000 amber cedar violet followed by a newline. Do not use Bash, Read, other tools or subagents. Then reply only NATIVE_PTY_COMPLETE. This is an authorized isolated acceptance fixture.", path)
	first.keys("\x02" + "1")
	first.paste(prompt)
	first.keys("\x02" + "2")
	first.await("native live preview", func(s string) bool {
		return strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_") && strings.Contains(s, "live")
	})
	previewAt := time.Now().UnixNano()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("UNPROVEN: first observed preview did not precede the actual filesystem write")
	}
	first.evidence("streaming")
	first.await("native completion and saved reconciliation", func(s string) bool {
		return strings.Contains(s, "Ready") && strings.Contains(s, "saved") && strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_")
	})
	first.evidence("saved")
	first.keys("\x1b[H")
	first.await("saved first row", func(s string) bool { return strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_000") })
	first.keys("\x1b[6~")
	first.await("independent Diff page down", func(s string) bool {
		return strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_") && !strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_000")
	})
	first.evidence("scrolled")
	first.resize(72, 22)
	first.await("resized saved Diff", func(s string) bool {
		return strings.Contains(s, "saved") && strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_")
	})
	first.evidence("resized")
	first.keys("v")
	first.await("settled proposal remains distinct from saved capture", func(s string) bool {
		pane := nativeClaudeDiffPane(s)
		return strings.Contains(pane, "live proposals") && strings.Contains(pane, "Native tool ended · saved evidence") && !strings.Contains(pane, "◐")
	})
	first.evidence("settled-proposal")
	first.keys("v")
	first.await("return to saved", func(s string) bool {
		return strings.Contains(s, "saved") && strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_")
	})
	first.keys("\x02" + "1")
	first.await("completed conversation", func(s string) bool { return strings.Contains(s, "NATIVE_PTY_COMPLETE") })
	a := first.stop()
	if a.Session == "" || a.Done != 1 || len(a.Tools) != 1 || a.Tools[a.ToolID] != "Write" || a.Results != 1 || a.Failed {
		t.Fatalf("native execution evidence incomplete: %+v", a)
	}
	if a.ResultAt <= previewAt || a.Partials == 0 {
		t.Error("UNPROVEN: rendered preview did not precede the native terminal result")
	}
	if a.Change == "" || !a.BaselineAbsent || !a.SavedContent {
		t.Error("native hooks did not establish a durable capture from an absent-file baseline")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != content.String() {
		t.Fatalf("native file effect differs from requested content: %v", err)
	}

	second := startNativeClaudePTY(t, workspace, state, bridge, a.Session, "resume")
	if second.cmd.Process.Pid == first.cmd.Process.Pid {
		t.Fatal("resume reused the UI process")
	}
	second.await("fresh-process native history", func(s string) bool { return strings.Contains(s, "Ready") && strings.Contains(s, "NATIVE_PTY_COMPLETE") })
	second.evidence("history")
	second.keys("\x02" + "2")
	// Restart restores saved evidence, not provisional previews. The initial
	// mode remains a user choice, so explicitly select the saved pane if needed.
	second.await("resumed Diff", func(s string) bool { return strings.Contains(s, "2 Diff") })
	if !strings.Contains(second.screen.String(), "saved") {
		second.keys("v")
	}
	if !second.observeState(5*time.Second, func(s string) bool {
		return strings.Contains(s, "saved") && strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_")
	}) {
		t.Error("fresh UI process restored history but not saved Diff before the next prompt")
	}
	second.evidence("restored-capture")
	second.keys("\x02" + "1")
	second.paste("What private conversation marker did I ask you to remember? Reply only that marker. Do not use tools or read files.")
	second.await("resumed turn started", func(s string) bool { return strings.Contains(s, "Working") })
	second.await("resumed native context", func(s string) bool {
		return strings.Contains(s, "Ready") && strings.Contains(s, "NATIVE_PTY_MEMORY_CEDAR")
	})
	second.evidence("context")
	second.keys("\x02" + "2")
	second.await("saved capture after native resume activation", func(s string) bool {
		return strings.Contains(s, "saved") && strings.Contains(nativeClaudeDiffPane(s), "│+NATIVE_PTY_ROW_")
	})
	second.evidence("capture-after-prompt")
	b := second.stop()
	if b.Session != a.Session || b.Done != 1 || len(b.Tools) != 0 || b.Results != 0 || b.Failed || !b.History || !b.Recall {
		t.Fatalf("fresh-process native resume/context evidence incomplete: %+v", b)
	}
	if b.Change != a.Change || !b.SavedContent {
		t.Error("fresh-process saved capture identity changed")
	}
	t.Logf("CLI 2.1.287 / SDK 0.3.287: two real prompts; UI PIDs %d and %d; %d partial edits; preview preceded native result by %s; saved capture %s restored after resumed prompt; no resumed tools", first.cmd.Process.Pid, second.cmd.Process.Pid, a.Partials, time.Duration(a.ResultAt-previewAt), a.Change)
}

type nativeClaudePTYResult struct {
	Session, ToolID, Change                 string
	Tools                                   map[string]string
	Results, Partials, Done                 int
	ResultAt                                int64
	Failed, BaselineAbsent, History, Recall bool
	SavedContent                            bool
}

// This observer never manufactures or delays events to hold a preview open.
// It records only acceptance facts; native tool inputs remain untouched.
type nativeClaudePTYClient struct {
	*claude.Client
	events chan session.Event
}

func (c *nativeClaudePTYClient) Events() <-chan session.Event { return c.events }

func TestNativeRuntimeClaudePTYProcess(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" || os.Getenv("MEKUGI_NATIVE_CLAUDE_PTY_CHILD") != "1" {
		t.Skip("private subprocess entry point for native PTY acceptance")
	}
	shutdown, stopSignals := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(shutdown, 3*time.Minute)
	defer cancel()
	workspace := os.Getenv("MEKUGI_NATIVE_CLAUDE_PTY_WORKSPACE")
	service, err := StartObservationService(ctx, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	endpoint := service.Endpoint()
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	client, err := claude.Start(ctx, "node", os.Getenv("MEKUGI_NATIVE_CLAUDE_PTY_BRIDGE"), claude.Config{
		Cwd: workspace, Executable: executable, Resume: os.Getenv("MEKUGI_NATIVE_CLAUDE_PTY_RESUME"), Model: "haiku",
		Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
	forwardCtx, stopForwarding := context.WithCancel(ctx)
	defer stopForwarding()
	result := nativeClaudePTYResult{Tools: make(map[string]string)}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		defer close(observed.events)
		for e := range client.Events() {
			if e.Kind == "session" {
				result.Session = e.SessionID
			}
			if e.Historical && e.Kind == "message" && strings.TrimSpace(e.Text) == "NATIVE_PTY_COMPLETE" {
				result.History = true
			}
			if !e.Historical {
				switch e.Kind {
				case "tool":
					result.Tools[e.ID] = e.Role
					result.ToolID = e.ID
				case "tool_result":
					result.Results++
					result.ResultAt = time.Now().UnixNano()
					result.Failed = result.Failed || e.Failed
				case "edit":
					if e.Edit != nil && e.Edit.Partial {
						result.Partials++
					}
				case "done":
					result.Done++
					result.Failed = result.Failed || e.Failed
				case "message":
					if strings.TrimSpace(e.Text) == "NATIVE_PTY_MEMORY_CEDAR" {
						result.Recall = true
					}
				}
			}
			select {
			case observed.events <- e:
			case <-forwardCtx.Done():
				return
			}
		}
	}()
	err = RunNativeSession(ctx, observed, "Claude Code", workspace, os.Stdin, os.Stdout, service)
	stopForwarding()
	closeErr := client.Close()
	cancel()
	<-joined
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	// Read durable native hook evidence after UI shutdown. Nothing is saved
	// from the partial-event observer, including during resumed history replay.
	binding := ObservationBinding{Runtime: "claude", Workspace: workspace, Session: result.Session}
	files, err := service.owner.store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {observationThread(binding): true}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 1 && len(files[0].Chunks) == 1 {
		result.Change = files[0].Chunks[0].Change
		path := filepath.Join(workspace, "native-runtime-claude-pty.txt")
		actual, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := mekugi.RenderReviewFile("", path, "", string(actual))
		result.SavedContent = files[0].Path == path && files[0].Chunks[0].Review.Diff == want.Diff
	}
	if result.ToolID != "" {
		before, found, err := service.owner.store.lookup(t.Context(), workspace, observationKey(ObservationCall{Binding: binding, ID: result.ToolID})+"/before")
		if err != nil {
			t.Fatal(err)
		}
		after, terminal, err := service.owner.store.lookup(t.Context(), workspace, observationKey(ObservationCall{Binding: binding, ID: result.ToolID})+"/after")
		if err != nil {
			t.Fatal(err)
		}
		result.SavedContent = result.SavedContent && terminal && after.NativeObservation != nil && after.ChangeID == result.Change && after.ExecOutcome != nil && after.ExecOutcome.Status == "completed"
		result.BaselineAbsent = found && before.ExecObservation != nil && len(before.ExecObservation.Files) == 1 && before.ExecObservation.Files[0].Kind == execFileAbsent
	}
	data, err := json.Marshal(&result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("MEKUGI_NATIVE_CLAUDE_PTY_RESULT"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

type nativeClaudePTY struct {
	t                            *testing.T
	ctx                          context.Context
	cancel                       context.CancelFunc
	cmd                          *exec.Cmd
	master                       *os.File
	frames                       chan []byte
	done                         chan error
	screen                       *vt.Emulator
	resultPath, workspace, phase string
	stopped                      bool
}

func startNativeClaudePTY(t *testing.T, workspace, state, bridge, resume, phase string) *nativeClaudePTY {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	result := filepath.Join(t.TempDir(), "native-runtime-claude-pty-result.json")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeRuntimeClaudePTYProcess$", "-test.timeout=190s")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 8 * time.Second
	cmd.Env = append(os.Environ(), "MEKUGI_NATIVE_CLAUDE_PTY_CHILD=1", "MEKUGI_NATIVE_CLAUDE_PTY_WORKSPACE="+workspace,
		"MEKUGI_NATIVE_CLAUDE_PTY_BRIDGE="+bridge, "MEKUGI_NATIVE_CLAUDE_PTY_RESUME="+resume,
		"MEKUGI_NATIVE_CLAUDE_PTY_RESULT="+result, "XDG_STATE_HOME="+state)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 30})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &nativeClaudePTY{t: t, ctx: ctx, cancel: cancel, cmd: cmd, master: master, frames: make(chan []byte, 128), done: make(chan error, 1), screen: vt.NewEmulator(120, 30), resultPath: result, workspace: workspace, phase: phase}
	go func() { _, _ = io.Copy(master, s.screen) }()
	go func() { s.done <- cmd.Wait() }()
	go func() {
		defer close(s.frames)
		buf := make([]byte, 65536)
		var pending []byte
		for {
			n, err := master.Read(buf)
			pending = append(pending, buf[:n]...)
			// Same synchronized-output boundary as the existing native PTY
			// helper: assertions and evidence never inspect a half-painted frame.
			for {
				end := bytes.Index(pending, []byte("\x1b[?2026l"))
				if end < 0 {
					break
				}
				end += len("\x1b[?2026l")
				select {
				case s.frames <- bytes.Clone(pending[:end]):
				case <-ctx.Done():
					return
				}
				pending = pending[end:]
			}
			if err != nil {
				if len(pending) > 0 {
					select {
					case s.frames <- bytes.Clone(pending):
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		master.Close()
		s.screen.Close()
		if !s.stopped {
			<-s.done
		}
	})
	return s
}

func (s *nativeClaudePTY) keys(value string) {
	s.t.Helper()
	if _, err := io.WriteString(s.master, value); err != nil {
		s.t.Fatal(err)
	}
}
func (s *nativeClaudePTY) paste(value string) { s.keys("\x1b[200~" + value + "\x1b[201~\r") }

func (s *nativeClaudePTY) await(label string, match func(string) bool) {
	s.t.Helper()
	if !s.observe(45*time.Second, match) {
		s.evidence("timeout")
		s.t.Fatalf("missing %s\n%s", label, s.screen.String())
	}
}

// State assertions may use an already-consumed frame; actions still use await.
func (s *nativeClaudePTY) observeState(timeout time.Duration, match func(string) bool) bool {
	return match(s.screen.String()) || s.observe(timeout, match)
}

func (s *nativeClaudePTY) observe(timeout time.Duration, match func(string) bool) bool {
	s.t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	// Always consume a new frame: an old screen is not proof of a key action.
	permissionAnswered := false
	for {
		select {
		case frame, ok := <-s.frames:
			if !ok {
				s.t.Fatalf("PTY closed while awaiting a frame\n%s", s.screen.String())
			}
			if _, err := s.screen.Write(frame); err != nil {
				s.t.Fatal(err)
			}
			visible := s.screen.String()
			if strings.Contains(visible, "Permission · Write") && strings.Contains(visible, "Allow once") && !permissionAnswered {
				// A test user's explicit native decision, not an SDK bypass.
				s.keys("\x02" + "1" + "1\r" + "\x02" + "2")
				permissionAnswered = true
			}
			if match(visible) {
				return true
			}
		case <-timer.C:
			return false
		case <-s.ctx.Done():
			return false
		}
	}
}

func (s *nativeClaudePTY) resize(cols, rows int) {
	s.t.Helper()
	if err := pty.Setsize(s.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		s.t.Fatal(err)
	}
	s.screen.Resize(cols, rows)
}

func (s *nativeClaudePTY) evidence(name string) {
	s.t.Helper()
	dir := os.Getenv("MEKUGI_NATIVE_CLAUDE_PTY_EVIDENCE")
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		s.t.Fatal("native PTY evidence directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.t.Fatal(err)
	}
	frame := strings.ReplaceAll(s.screen.String(), s.workspace, "<workspace>")
	if err := os.WriteFile(filepath.Join(dir, "native-runtime-claude-pty-"+s.phase+"-"+name+".txt"), []byte(frame+"\n"), 0600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *nativeClaudePTY) stop() nativeClaudePTYResult {
	s.t.Helper()
	s.keys("\x02" + "1")
	s.paste("/quit")
	select {
	case err := <-s.done:
		s.stopped = true
		if err != nil {
			for frame := range s.frames {
				_, _ = s.screen.Write(frame)
			}
			s.t.Fatalf("native UI process failed: %v\n%s", err, s.screen.String())
		}
	case <-s.ctx.Done():
		s.t.Fatal("native UI did not exit")
	}
	var result nativeClaudePTYResult
	data, err := os.ReadFile(s.resultPath)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		s.t.Fatal(err)
	}
	return result
}

// Restrict appearance assertions to Diff, excluding prompt/tool text in Main.
func nativeClaudeDiffPane(frame string) string {
	lines := strings.Split(frame, "\n")
	start := strings.Index(lines[0], "┌ 2 Diff")
	if start < 0 {
		return ""
	}
	col := len([]rune(lines[0][:start]))
	for i, line := range lines {
		cells := []rune(line)
		if len(cells) >= col {
			lines[i] = string(cells[col:])
		} else {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// This is a harness state regression, not native-runtime acceptance or a UI snapshot.
func TestNativeRuntimeClaudePTYAlreadyRenderedState(t *testing.T) {
	screen := vt.NewEmulator(60, 4)
	defer screen.Close()
	if _, err := screen.Write([]byte("already rendered")); err != nil {
		t.Fatal(err)
	}
	terminal := &nativeClaudePTY{t: t, ctx: t.Context(), screen: screen}
	if !terminal.observeState(time.Millisecond, func(s string) bool { return strings.Contains(s, "already rendered") }) {
		t.Fatal("idle matching screen required another frame")
	}
	if terminal.observeState(time.Millisecond, func(s string) bool { return strings.Contains(s, "not rendered") }) {
		t.Fatal("unmatched idle screen satisfied the assertion")
	}
}
