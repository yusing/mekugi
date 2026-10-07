package claude

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
)

// The provider records context but never asks for a tool. Only native user-shell
// input may produce fixture effects; transcript-only appends must not query it.
func TestClaudeNativeUserShell(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge; local scripted provider only")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-user-shell-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	var mu sync.Mutex
	var packets []string
	mainStarted, mainRelease := make(chan struct{}), make(chan struct{})
	var startedOnce sync.Once
	cancelMainStarted, cancelMainDropped := make(chan struct{}), make(chan struct{})
	cancelMainGate := false
	cancelProviderStop := make(chan struct{})
	stopCancelProvider := sync.OnceFunc(func() { close(cancelProviderStop) })
	defer stopCancelProvider()
	releaseMain := sync.OnceFunc(func() { close(mainRelease) })
	defer releaseMain()
	sideStarted, sideRelease, sideDropped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseSide := sync.OnceFunc(func() { close(sideRelease) })
	defer releaseSide()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		messages, _ := json.Marshal(packet["messages"])
		var contextText strings.Builder
		for _, value := range packet["messages"].([]any) {
			message := value.(map[string]any)
			switch content := message["content"].(type) {
			case string:
				contextText.WriteString(content)
			case []any:
				for _, block := range content {
					if text, ok := block.(map[string]any)["text"].(string); ok {
						contextText.WriteString(text)
					}
				}
			}
			contextText.WriteByte('\n')
		}
		mu.Lock()
		packets = append(packets, contextText.String())
		gateCancel := strings.Contains(string(messages), "USER_SHELL_CANCEL_ACTIVE_MAIN") && !cancelMainGate
		if gateCancel {
			cancelMainGate = true
		}
		mu.Unlock()
		if strings.Contains(contextText.String(), "USER_SHELL_SIDE_FIRST") && !strings.Contains(contextText.String(), "USER_SHELL_SIDE_FOLLOWUP") {
			close(sideStarted)
			select {
			case <-sideRelease:
			case <-r.Context().Done():
				close(sideDropped)
				return
			}
			nativeAgentProviderReply(w, packet, []any{map[string]any{"type": "text", "text": "USER_SHELL_SIDE_ANSWER"}}, "end_turn")
			return
		}
		if gateCancel {
			close(cancelMainStarted)
			select {
			case <-r.Context().Done():
			case <-cancelProviderStop:
			}
			close(cancelMainDropped)
			return
		}
		if strings.Contains(string(messages), "USER_SHELL_ACTIVE_MAIN") {
			startedOnce.Do(func() { close(mainStarted) })
			select {
			case <-mainRelease:
			case <-r.Context().Done():
				return
			}
		}
		nativeAgentProviderReply(w, packet, []any{map[string]any{"type": "text", "text": "USER_SHELL_CONTEXT_ACCEPTED"}}, "end_turn")
	}))
	defer func() { stopCancelProvider(); releaseMain(); releaseSide(); provider.Close() }()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	h := &nativeDecisionsHarness{ctx: ctx, workspace: t.TempDir()}
	defer func() {
		if h.client != nil {
			h.close(t)
		}
	}()
	id := ""
	mainDone := 0
	var history []session.ShellResult
	consume := func(e session.Event) session.Event {
		if e.Kind == "session" && e.SideID == "" {
			id = e.SessionID
		}
		if e.Kind == "shell_started" && e.Shell != nil {
			id = e.Shell.SessionID
		}
		if e.Kind == "done" && !e.Historical && e.SideID == "" {
			mainDone++
		}
		if e.Kind == "shell_done" && e.Historical && e.Shell != nil {
			history = append(history, *e.Shell)
		}
		if e.Kind == "prompt" || e.Kind == "tool" {
			t.Fatalf("user shell unexpectedly entered model tool/permission lifecycle: %+v", e)
		}
		if e.SideID != "" && (e.Kind == "side_closed" || e.Kind == "error" || e.Failed) {
			t.Fatalf("Main shell cancellation retired independent side: %+v", e)
		}
		return e
	}
	next := func() session.Event { return consume(h.next(t)) }
	start := func(resume string, fork bool) {
		var err error
		h.client, err = Start(ctx, node, bridge, Config{Cwd: h.workspace, Executable: executable, Resume: resume, ForkSession: fork, Model: "haiku"})
		if err != nil {
			t.Fatal(err)
		}
		for next().Kind != "ready" {
		}
	}
	requests := func() int { mu.Lock(); defer mu.Unlock(); return len(packets) }
	run := func(label, command, output string, exit int) {
		t.Helper()
		t.Logf("native shell phase %s", label)
		before, doneBefore := requests(), mainDone
		if err := h.client.RunShell(ctx, session.ShellCommand{ID: label, SessionID: id, Command: command}); err != nil {
			t.Fatal(err)
		}
		started := false
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for {
			var e session.Event
			select {
			case event, ok := <-h.client.Events():
				if !ok {
					t.Fatal("native shell bridge closed")
				}
				e = consume(event)
			case <-deadline.C:
				t.Fatalf("%s did not settle native shell and transcript append", label)
			}
			if e.Kind == "shell_started" && e.Shell != nil && e.Shell.ID == label {
				started = true
			}
			if e.Kind != "shell_done" || (e.Shell == nil || e.Shell.ID != label) {
				continue
			}
			if !started || e.Failed || e.Shell == nil || !e.Shell.Retained || e.Shell.Command != command || e.Shell.Output != output || e.Shell.ExitCode == nil || *e.Shell.ExitCode != exit {
				t.Fatalf("%s native shell result: started=%t event=%+v shell=%+v", label, started, e, e.Shell)
			}
			break
		}
		if requests() != before || mainDone != doneBefore {
			t.Fatal("shell or history append entered Main inference/completion")
		}
	}
	query := func(marker string, expected ...string) {
		t.Helper()
		before := requests()
		if err := h.client.Send(ctx, marker); err != nil {
			t.Fatal(err)
		}
		for next().Kind != "done" {
		}
		mu.Lock()
		defer mu.Unlock()
		if len(packets) != before+1 {
			t.Fatalf("Main requested %d provider calls", len(packets)-before)
		}
		for _, want := range expected {
			if !strings.Contains(packets[len(packets)-1], want) {
				t.Fatalf("%s lost native shell context %q", marker, want)
			}
		}
	}
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	cancelShell := func(label string) string {
		t.Helper()
		before := requests()
		command := "printf 'CANCEL_" + label + "<&>🌲\\n'; echo $$ > shell-cancel-" + label + ".pid; while [ ! -f shell-release-" + label + " ]; do sleep 0.05; done; printf forbidden > shell-late-" + label
		if err := h.client.RunShell(ctx, session.ShellCommand{ID: "cancel/" + label, SessionID: id, Command: command}); err != nil {
			t.Fatal(err)
		}
		for e := next(); e.Kind != "shell_started" || e.Shell == nil || e.Shell.ID != "cancel/"+label; e = next() {
		}
		var pid int
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			if data, err := os.ReadFile(filepath.Join(h.workspace, "shell-cancel-"+label+".pid")); err == nil {
				pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if pid == 0 {
			t.Fatal("native cancellation process did not start")
		}
		defer func() {
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}()
		if err := h.client.Interrupt(ctx); err != nil {
			t.Fatal(err)
		}
		var result *session.ShellResult
		restarted := false
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for result == nil || !restarted {
			select {
			case event, ok := <-h.client.Events():
				if !ok {
					t.Fatal("native cancellation bridge closed")
				}
				e := consume(event)
				if e.Kind == "error" {
					t.Fatalf("native cancellation error: %s", e.Text)
				}
				if e.Kind == "shell_restarted" {
					restarted = true
				}
				if e.Kind == "shell_done" && e.Shell != nil && e.Shell.ID == "cancel/"+label {
					if result != nil || e.Failed || !e.Shell.Retained || e.Shell.Command != command || e.Shell.ExitCode == nil || *e.Shell.ExitCode == 0 {
						t.Fatalf("native cancellation result: %+v %+v", e, e.Shell)
					}
					result = e.Shell
				}
			case <-deadline.C:
				t.Fatalf("native cancellation did not restart and retain %s; pid probe=%v", label, syscall.Kill(pid, 0))
			}
		}
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Fatalf("cancelled native process alive: %v", err)
		}
		pid = 0
		if requests() != before {
			t.Fatal("native shutdown/resume queried provider")
		}
		h.write(t, "shell-release-"+label, "")
		if _, err := os.Stat(filepath.Join(h.workspace, "shell-late-"+label)); !os.IsNotExist(err) {
			t.Fatalf("cancelled native effect: %v", err)
		}
		t.Logf("native cancelled %s output=%q exit=%d", label, result.Output, *result.ExitCode)
		stdout := "CANCEL_" + label + "<&>🌲"
		stderr, ok := strings.CutPrefix(result.Output, stdout)
		if !ok || !strings.Contains(stderr, "The operation was aborted") {
			t.Fatalf("native cancellation lost expected stdout/stderr: %q", result.Output)
		}
		return "<bash-stdout>" + escaped(stdout) + "</bash-stdout><bash-stderr>" + escaped(stderr) + "</bash-stderr><bash-exit-code>" + strconv.Itoa(*result.ExitCode) + "</bash-exit-code>"
	}
	start("", false)
	firstCancelOutput := cancelShell("before-main")
	const special = "<bash-stdout>完整🌲 & café</bash-stdout>"
	const first = "printf '%s\\n' '<bash-stdout>完整🌲 & café</bash-stdout>'; printf 'first\\n' >> shell-effects.txt"
	run("first", first, special, 0)
	parent := id
	if parent == "" {
		t.Fatal("initial shell did not establish native session")
	}
	query("USER_SHELL_FIRST_MAIN", firstCancelOutput, "&lt;bash-stdout&gt;完整🌲 &amp; café&lt;/bash-stdout&gt;", "shell-effects.txt")
	run("failure", "printf 'partial\\n'; printf 'stderr<&>\\n' >&2; printf 'failure\\n' >> shell-effects.txt; exit 7", "partialstderr<&>", 7)
	query("USER_SHELL_LATER_MAIN", "partial", "stderr&lt;&amp;&gt;", "<bash-exit-code>7</bash-exit-code>")
	// The UI admits shell input during Main inference. Native shell and its
	// transcript-only append must settle without settling or swallowing Main.
	doneBefore := mainDone
	if err := h.client.Send(ctx, "USER_SHELL_ACTIVE_MAIN"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-mainStarted:
	case <-ctx.Done():
		t.Fatal("Main provider did not start")
	}
	before := requests()
	const activeCommand = "printf 'active-main\\n'; printf 'active\\n' >> shell-effects.txt"
	if err := h.client.RunShell(ctx, session.ShellCommand{ID: "during-main", SessionID: id, Command: activeCommand}); err != nil {
		t.Fatal(err)
	}
	for e := next(); e.Kind != "shell_started" || e.Shell == nil || e.Shell.ID != "during-main"; e = next() {
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		data, err := os.ReadFile(filepath.Join(h.workspace, "shell-effects.txt"))
		if err == nil && string(data) == "first\nfailure\nactive\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell effect missing while Main provider remains gated: %q %v", data, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	settledShell := false
	acceptShell := func(e session.Event) {
		if e.Kind != "shell_done" || e.Shell == nil || e.Shell.ID != "during-main" {
			return
		}
		if settledShell || e.Failed || !e.Shell.Retained || e.Shell.Command != activeCommand || e.Shell.Output != "active-main" || e.Shell.ExitCode == nil || *e.Shell.ExitCode != 0 {
			t.Fatalf("active-Main shell result: %+v %+v", e, e.Shell)
		}
		settledShell = true
	}
	// Drain ready native frames before releasing Main. A transcript-only result
	// must not masquerade as completion of the still-gated model response.
	draining := true
	for draining {
		select {
		case e, ok := <-h.client.Events():
			if !ok {
				t.Fatal("bridge closed while Main provider remained gated")
			}
			acceptShell(consume(e))
		default:
			draining = false
		}
	}
	if mainDone != doneBefore || requests() != before {
		t.Fatal("shell settled or queried gated Main")
	}
	releaseMain()
	settlementDeadline := time.NewTimer(10 * time.Second)
	defer settlementDeadline.Stop()
	for !settledShell || mainDone == doneBefore {
		select {
		case e, ok := <-h.client.Events():
			if !ok {
				t.Fatal("bridge closed during Main/shell settlement")
			}
			acceptShell(consume(e))
		case <-settlementDeadline.C:
			t.Fatalf("Main/shell did not independently settle: Main completions=%d shell=%t", mainDone-doneBefore, settledShell)
		}
	}
	if mainDone != doneBefore+1 || requests() != before {
		t.Fatal("shell append altered Main lifecycle or queried provider")
	}
	query("USER_SHELL_AFTER_ACTIVE_MAIN", "active-main", "USER_SHELL_ACTIVE_MAIN")
	h.wantFile(t, "shell-effects.txt", "first\nfailure\nactive\n")
	t.Log("shell during active Main retained exact effect/context and independent completion")

	if err := h.client.Close(); err != nil {
		t.Fatal(err)
	}
	id = parent
	start(parent, false)
	if len(history) < 2 {
		t.Fatalf("fresh resume restored %d shell rows", len(history))
	}
	query("USER_SHELL_FRESH_RESUME", "USER_SHELL_FIRST_MAIN", "partial", "&lt;bash-stdout&gt;完整🌲")
	h.wantFile(t, "shell-effects.txt", "first\nfailure\nactive\n")
	if err := h.client.Close(); err != nil {
		t.Fatal(err)
	}
	start(parent, true)
	query("USER_SHELL_FORK", "partial", "&lt;bash-stdout&gt;完整🌲")
	if id == parent || id == "" {
		t.Fatal("native fork did not acquire independent identity")
	}
	run("fork", "printf 'fork\\n'; printf 'fork\\n' >> shell-effects.txt", "fork", 0)
	h.wantFile(t, "shell-effects.txt", "first\nfailure\nactive\nfork\n")
	if err := h.client.Close(); err != nil {
		t.Fatal(err)
	}
	id = parent
	start(parent, false)
	query("USER_SHELL_PARENT_ISOLATED", "partial")
	mu.Lock()
	borrowed := strings.Contains(packets[len(packets)-1], "printf 'fork")
	mu.Unlock()
	if borrowed {
		t.Fatal("parent borrowed fork shell context")
	}
	t.Log("initial/later shell, nonzero exit, exact escaped Unicode context, fresh resume, fork and parent isolation passed without shell inference or replay")
	const sideID = "shell-cancel-side"
	if err := h.client.SendSide(ctx, session.SideInput{ID: sideID, Source: parent, Input: []session.InputPart{{Text: "USER_SHELL_SIDE_FIRST"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sideStarted:
	case <-ctx.Done():
		t.Fatal("side did not reach native provider before Main cancellation")
	}
	if err := h.client.Send(ctx, "USER_SHELL_CANCEL_ACTIVE_MAIN"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelMainStarted:
	case <-ctx.Done():
		t.Fatal("cancellation Main did not reach provider")
	}
	busyOutput := cancelShell("busy")
	select {
	case <-cancelMainDropped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown retained the gated Main request")
	}
	select {
	case <-sideDropped:
		t.Fatal("Main shell cancellation dropped the active native side request")
	default:
	}
	releaseSide()
	for e := next(); e.Kind != "done" || e.SideID != sideID; e = next() {
	}
	query("USER_SHELL_AFTER_BUSY_CANCEL", "USER_SHELL_PARENT_ISOLATED", "partial", busyOutput, "<bash-exit-code>")
	if err := h.client.SendSide(ctx, session.SideInput{ID: sideID, Source: parent, Input: []session.InputPart{{Text: "USER_SHELL_SIDE_FOLLOWUP"}}}); err != nil {
		t.Fatal(err)
	}
	for e := next(); e.Kind != "done" || e.SideID != sideID; e = next() {
	}
	mu.Lock()
	sideContext := packets[len(packets)-1]
	mu.Unlock()
	for _, want := range []string{"USER_SHELL_SIDE_FIRST", "USER_SHELL_SIDE_ANSWER", "USER_SHELL_SIDE_FOLLOWUP", "USER_SHELL_PARENT_ISOLATED"} {
		if !strings.Contains(sideContext, want) {
			t.Fatalf("native side follow-up lost prior context %q", want)
		}
	}
	for _, forbidden := range []string{"USER_SHELL_CANCEL_ACTIVE_MAIN", "USER_SHELL_AFTER_BUSY_CANCEL", "CANCEL_busy"} {
		if strings.Contains(sideContext, forbidden) {
			t.Fatalf("native side follow-up borrowed later Main context %q", forbidden)
		}
	}
	t.Log("active native side survived Main shell cancellation and retained isolated follow-up context")
	if err := h.client.Close(); err != nil {
		t.Fatal(err)
	}
	id = parent
	start(parent, false)
	query("USER_SHELL_CANCEL_FRESH_RESUME", firstCancelOutput, busyOutput, "USER_SHELL_AFTER_BUSY_CANCEL", "partial")
	h.wantFile(t, "shell-effects.txt", "first\nfailure\nactive\nfork\n")
	t.Logf("native before-Main and active-Main shutdown/resume, prior/cancellation context, fresh resume and exactly-once effects passed with %d local provider requests", requests())
}
