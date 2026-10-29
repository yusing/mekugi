package launch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Unrelated publications must neither restart a bound image read nor hide
// hydrated content while a newer revision loads. Resource responses are held
// in front of the actual connector, so reads and browser cancellations are
// counted where they reach the machine.
func TestWebUIBrowserContentReads(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := os.Getenv("MEKUGI_TEST_AGENT_BROWSER")
	if browser == "" {
		browser = "agent-browser"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	token := strings.Repeat("d", 64)
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Content", token)
	var imageReads, imageAborts, contentReads atomic.Int32
	var holdImage, holdContent atomic.Bool
	holdImage.Store(true)
	release := make(chan struct{}, 4)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (r.URL.Path == "/api/resources/image" && holdImage.Load() || r.URL.Path == "/api/resources/content" && holdContent.Load()) {
			// The server notices a canceled browser read only after the
			// request body is consumed.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			image := r.URL.Path == "/api/resources/image"
			if image {
				imageReads.Add(1)
			} else {
				contentReads.Add(1)
			}
			select {
			case <-release:
			case <-r.Context().Done():
				if image {
					imageAborts.Add(1)
				}
				return
			case <-ctx.Done():
				return
			}
		}
		machine.bridge.Load().ServeHTTP(w, r)
	}))
	defer func() {
		remote.CloseClientConnections()
		remote.Close()
	}()

	const pixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="
	// Sequence 1 lies outside the latest feed window, so changing it replaces
	// the browser's complete window with new entry objects.
	entries := make([]activitypresentation.Entry, 0, 63)
	for seq := uint64(1); seq <= 60; seq++ {
		entries = append(entries, activitypresentation.Entry{Facts: activitypresentation.Facts{Seq: seq, Agent: "Main", Kind: "text", Text: fmt.Sprintf("Earlier step %d.", seq)}, Revision: 1})
	}
	entries = append(entries,
		activitypresentation.Entry{Facts: activitypresentation.Facts{Seq: 61, Agent: "You", Kind: "text", Text: "Bound image", Images: []string{pixel}}, Revision: 1},
		activitypresentation.Entry{Facts: activitypresentation.Facts{Seq: 62, Agent: "Main", Kind: "text", Text: "Status 1"}, Revision: 1},
	)
	frame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: "Content thread", Workspace: machine.workdir}, Input: &sessionapi.Input{Stoppable: true}}
	publish := func() {
		t.Helper()
		frame.Main = &activitypresentation.Publication{Entries: append([]activitypresentation.Entry(nil), entries...)}
		if err := machine.publishFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	set := func(seq uint64, text string) {
		entries[seq-1].Text = text
		entries[seq-1].Revision++
	}
	publish()

	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-content-reads").CombinedOutput()
	if err != nil {
		t.Fatalf("browser identity: %v %s", err, identity)
	}
	name := strings.TrimSpace(string(identity))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, browser, "--session", name, "close").Run()
	}()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, browser, append([]string{"--session", name}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("browser %s: %v %s", args[0], err, out)
		}
		return string(out)
	}
	await := func(what string, done func() bool) {
		t.Helper()
		for !done() {
			select {
			case <-ctx.Done():
				t.Fatalf("waiting for %s: %v", what, ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	// A restarted read aborts and reacquires after the browser renders, so
	// give that path time to reach the connector before counting.
	settled := func(text string, reads, aborts int32) {
		t.Helper()
		run("wait", "--text", text)
		run("wait", "300")
		if got, canceled := imageReads.Load(), imageAborts.Load(); got != reads || canceled != aborts {
			t.Fatalf("after %q image reads/aborts = %d/%d, want %d/%d", text, got, canceled, reads, aborts)
		}
	}

	run("open", page.URL)
	run("find", "label", "Machine name", "fill", "Content")
	run("find", "label", "Connector URL", "fill", remote.URL)
	run("find", "label", "Access token", "fill", token)
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("wait", "--text", "Content thread")
	run("find", "text", "Content thread", "click", "--exact")
	run("wait", "--text", "Loading image 1…")
	await("the image read", func() bool { return imageReads.Load() == 1 })

	set(62, "Status 2")
	publish()
	settled("Status 2", 1, 0)
	set(1, "Rewritten early step.")
	set(62, "Status 3")
	publish()
	settled("Status 3", 1, 0)
	// A new read attachment is a different source and restarts the read.
	frame.Attachment++
	set(62, "Status 4")
	publish()
	settled("Status 4", 2, 1)
	release <- struct{}{}
	run("wait", "--fn", `document.querySelector('img[alt="Attached image 1"]')?.naturalWidth === 1`)
	screenshot := func(file string) {
		if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
			run("screenshot", filepath.Join(artifacts, file))
		}
	}
	set(1, "Rewritten again.")
	set(62, "Status 5")
	publish()
	settled("Status 5", 2, 1)
	if run("eval", `document.querySelector('img[alt="Attached image 1"]')?.naturalWidth === 1`) != "true\n" {
		t.Fatal("unrelated publication replaced the loaded image")
	}
	screenshot("content-reads-image-after-window-replacement.png")

	// Oversized streamed text is published as a preview and hydrated by
	// separate reads; the tail is only visible in complete content.
	holdImage.Store(false)
	holdContent.Store(true)
	stream := strings.Repeat("Streamed prose. ", 300)
	entries = append(entries, activitypresentation.Entry{Facts: activitypresentation.Facts{Seq: 63, Agent: "Main", Kind: "text", Text: stream + "TAIL_ONE"}, Revision: 1})
	publish()
	run("wait", "--text", "Loading content…")
	await("the first content read", func() bool { return contentReads.Load() == 1 })
	release <- struct{}{}
	run("wait", "--fn", `document.body.innerText.includes('TAIL_ONE') && !document.body.innerText.includes('Loading content')`)
	for revision, tail := range []string{"TAIL_TWO", "TAIL_THREE"} {
		set(63, entries[62].Text+" "+tail)
		publish()
		await("the newer content read", func() bool { return contentReads.Load() == int32(revision+2) })
		if run("eval", `document.body.innerText.includes('TAIL_ONE') && !document.body.innerText.includes('Loading content')`) != "true\n" {
			t.Fatalf("hydrated content was hidden while %s loaded", tail)
		}
		screenshot(fmt.Sprintf("content-reads-hydrated-while-%s.png", strings.ToLower(tail)))
		release <- struct{}{}
		run("wait", "--fn", fmt.Sprintf(`document.body.innerText.includes(%q) && !document.body.innerText.includes('Loading content')`, tail))
	}
	// Content hydrated under another read attachment is never shown.
	frame.Attachment++
	set(63, entries[62].Text+" TAIL_FOUR")
	publish()
	await("the reattached content read", func() bool { return contentReads.Load() == 4 })
	run("wait", "--fn", `document.body.innerText.includes('Loading content') && !document.body.innerText.includes('TAIL_THREE')`)
	release <- struct{}{}
	run("wait", "--fn", `document.body.innerText.includes('TAIL_FOUR') && !document.body.innerText.includes('Loading content')`)
}
