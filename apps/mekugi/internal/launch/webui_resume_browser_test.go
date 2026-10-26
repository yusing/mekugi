package launch

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
)

// The destination owner deliberately holds attachment admission while the real
// browser continues editing and controlling the source through the connector.
func TestWebUIBrowserResumeFailure(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui, with agent-browser installed")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := os.Getenv("MEKUGI_TEST_AGENT_BROWSER")
	if browser == "" {
		browser = "agent-browser"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	type attachment struct {
		native  *sessionapi.Native
		request sessionapi.NativeRequest
	}
	attachments := make(chan attachment, 8)
	var destinationAttaches atomic.Int32
	machine := newWebUIBrowserMachineWithNative(t, ctx, page.URL, "Resume", strings.Repeat("c", 64), func(native *sessionapi.Native, request sessionapi.NativeRequest) bool {
		if !request.Attach {
			return false
		}
		attachments <- attachment{native, request}
		if request.Thread == "destination" {
			destinationAttaches.Add(1)
			return true
		}
		return false
	})
	machine.start("destination")
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-resume").CombinedOutput()
	if err != nil {
		t.Fatalf("browser session identity: %v: %s", err, identity)
	}
	name := strings.TrimSpace(string(identity))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, browser, "--session", name, "close").Run()
	}()
	run := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, browser, append([]string{"--session", name}, args...)...).CombinedOutput()
		if err != nil {
			snapshot, _ := exec.CommandContext(ctx, browser, "--session", name, "snapshot", "-i").CombinedOutput()
			t.Fatalf("browser %s failed: %v: %s\n%s", args[0], err, output, snapshot)
		}
		return string(output)
	}
	button := func(label string) { t.Helper(); run("find", "role", "button", "click", "--name", label, "--exact") }
	next := func(thread string) attachment {
		t.Helper()
		select {
		case value := <-attachments:
			if value.request.Thread != thread {
				t.Fatalf("attached %q, want %q", value.request.Thread, thread)
			}
			return value
		case <-ctx.Done():
			t.Fatal("timed out waiting for " + thread + " attachment")
			return attachment{}
		}
	}
	publish := func(target attachment, revision, effect uint64) {
		t.Helper()
		thread := sessionpresentation.Thread{ID: target.request.Thread, Caller: "/root"}
		frame := sessionapi.NativeFrame{View: sessionpresentation.View{
			Version: sessionpresentation.Version{Attachment: 1, Revision: revision},
			Main:    thread, Input: thread, Displayed: thread,
			Info: sessionpresentation.MainInfo{Workspace: machine.workdir},
		}}
		if effect != 0 {
			frame.Effects = []sessionapi.NativeEffect{{ID: effect, Reattach: new("destination")}}
		}
		if err := target.native.Publish(target.request.Attachment, frame); err != nil {
			t.Fatal(err)
		}
	}
	stopSource := func() {
		t.Helper()
		button("Stop")
		select {
		case intent := <-machine.intents:
			if intent.Action != sessionapi.Stop || intent.Thread != "same-thread" {
				t.Fatalf("wrong source control: %s %s", intent.Thread, intent.Action)
			}
		case <-ctx.Done():
			t.Fatal("source control did not reach owner")
		}
	}

	run("--engine", "chrome", "open", page.URL)
	run("snapshot", "-i")
	run("find", "label", "Machine name", "fill", "Resume")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("c", 64))
	button("Connect")
	run("wait", "--text", "Resume thread")
	run("find", "text", "Resume thread", "click", "--exact")
	run("wait", "--fn", "document.querySelector('button[aria-label=Stop]')?.disabled === false")
	source := next("same-thread")
	sourceURL := run("get", "url")
	run("find", "label", "Message", "fill", "keep this draft")
	upload := filepath.Join(t.TempDir(), "resume-note.txt")
	if err := os.WriteFile(upload, []byte("retained browser upload"), 0600); err != nil {
		t.Fatal(err)
	}
	run("upload", "input[type=file]", upload)
	run("wait", "--text", "resume-note.txt")
	publish(source, 2, 1)
	failed := next("destination")
	run("wait", "--text", "Opening resumed thread")
	run("find", "label", "Message", "fill", "keep this draft while attaching")
	stopSource()
	failed.request.Reply <- "fixture destination unavailable"
	run("wait", "--text", "Could not resume")
	select {
	case <-failed.request.ViewContext.Done():
	case <-ctx.Done():
		t.Fatal("failed candidate was not released")
	}
	if run("get", "url") != sourceURL {
		t.Fatal("failed resume changed the selected source")
	}
	run("wait", "--fn", "document.querySelector('#composer-text')?.value === 'keep this draft while attaching' && !document.querySelector('#composer-text').readOnly && !!document.querySelector('button[aria-label=\"Remove resume-note.txt\"]')")
	stopSource()
	// Republication of a retained effect must not retry the failed candidate.
	publish(source, 3, 1)
	run("find", "label", "Message", "fill", "source still editable after failure")
	stopSource()
	if got := destinationAttaches.Load(); got != 1 {
		t.Fatalf("failed retained effect attached %d times", got)
	}
	// A fresh explicit effect can succeed, adopting its original subscription.
	publish(source, 4, 2)
	destination := next("destination")
	publish(destination, 1, 0)
	destination.request.Reply <- ""
	run("wait", "--fn", "new URLSearchParams(location.hash.slice(1)).get('thread') === 'destination'")
	run("wait", "--fn", "!!document.querySelector('#composer-text') && !document.querySelector('#composer-text').readOnly")
	select {
	case <-source.request.ViewContext.Done():
	case <-ctx.Done():
		t.Fatal("successful resume did not release source")
	}
	// Publishing through the admitted attachment after keyed cleanup proves the
	// destination was retained, rather than closed and silently reattached.
	publish(destination, 2, 0)
	run("find", "label", "Message", "fill", "destination remains editable")
	button("Stop")
	select {
	case intent := <-machine.intents:
		if intent.Action != sessionapi.Stop || intent.Thread != "destination" {
			t.Fatalf("wrong adopted control: %s %s", intent.Thread, intent.Action)
		}
	case <-ctx.Done():
		t.Fatal("adopted destination control did not reach owner")
	}
	if got := destinationAttaches.Load(); got != 2 {
		t.Fatalf("destination was reattached after adoption: %d attempts", got)
	}
	select {
	case <-destination.request.ViewContext.Done():
		t.Fatal("keyed component cleanup closed adopted destination")
	default:
	}
}
