//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// Exercise the actual terminal bytes, not only the notification helper. Herdr
// observes these sequences after Codex has finished its app-server turn.
func TestAppServerNotificationsNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, &appPreviewProvider{}, nil, nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", `tui.notification_method="osc9"`, "-c", `tui.notification_condition="unfocused"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env, cmd.Dir = routerFaultCodexEnvironment(t), t.TempDir()
	outer, inner, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()
	defer inner.Close()
	if err := pty.Setsize(outer, &pty.Winsize{Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	wait, err := startAppServerUI(ctx, cmd, inner, inner, nil, nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	chunks := make(chan []byte, 128)
	go func() {
		defer close(chunks)
		buf := make([]byte, 65536)
		for {
			n, err := outer.Read(buf)
			if n > 0 {
				select {
				case chunks <- bytes.Clone(buf[:n]):
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
	go func() { _, _ = io.Copy(outer, screen) }()
	var raw bytes.Buffer
	ingest := func(chunk []byte) { raw.Write(chunk); _, _ = screen.Write(chunk) }
	await := func(label string, predicate func() bool) {
		t.Helper()
		for !predicate() {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					t.Fatalf("terminal closed before %s: %s", label, screen.String())
				}
				ingest(chunk)
			case err := <-done:
				t.Fatalf("UI exited before %s: %v\n%s", label, err, screen.String())
			case <-ctx.Done():
				t.Fatalf("timeout before %s: %v\n%s", label, ctx.Err(), screen.String())
			}
		}
	}
	await("startup", func() bool {
		return strings.Contains(screen.String(), "Ready") && bytes.Contains(raw.Bytes(), []byte("\x1b[?1003;1004;1006;2004h"))
	})
	if _, err := io.WriteString(outer, "\x1b[O"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(outer, "A deterministic preview prompt\r"); err != nil {
		t.Fatal(err)
	}
	const completion = "\x1b]9;Agent turn complete\a"
	await("completion and desktop notification", func() bool {
		return strings.Contains(screen.String(), "Recovered after a retry.") && strings.Contains(screen.String(), "╭─ Completed") && bytes.Contains(raw.Bytes(), []byte(completion))
	})
	if got := bytes.Count(raw.Bytes(), []byte(completion)); got != 1 {
		t.Fatalf("desktop notifications = %d, want one", got)
	}
	// Working sets an OSC 0 spinner, and the idle title removes it. The terminal
	// title is not part of VT screen text, so inspect the raw stream.
	working := regexp.MustCompile("\x1b\\]0;Mekugi [⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏] ")
	if !working.Match(raw.Bytes()) {
		t.Fatalf("missing working title in terminal output: %q", raw.Bytes())
	}
	await("idle title", func() bool {
		return bytes.LastIndex(raw.Bytes(), []byte("\x1b]0;Mekugi "+filepath.Base(cmd.Dir)+"\a")) > working.FindIndex(raw.Bytes())[0]
	})
	if _, err := io.WriteString(outer, "/quit\r"); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case chunk, ok := <-chunks:
			if ok {
				ingest(chunk)
				continue
			}
			goto closed
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			_ = inner.Close()
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
closed:
	if !bytes.Contains(raw.Bytes(), []byte("\x1b[?1003;1004;1006;2004l")) {
		t.Fatalf("focus reporting was not disabled on exit: %q", raw.Bytes())
	}
	if last := bytes.LastIndex(raw.Bytes(), []byte("\x1b]0;")); last < 0 || !bytes.HasPrefix(raw.Bytes()[last:], []byte("\x1b]0;\a")) {
		t.Fatalf("terminal title was not cleared on exit: %q", raw.Bytes()[max(0, raw.Len()-160):])
	}
}
