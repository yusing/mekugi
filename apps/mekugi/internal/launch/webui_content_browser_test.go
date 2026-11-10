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
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Unrelated publications must neither restart a bound image read nor hide
// hydrated content while a newer revision loads. Oversized activity details
// keep their body through refreshes of the same selection. Resource responses
// are held in front of the actual connector, so reads and browser
// cancellations are counted where they reach the machine.
func TestWebUIBrowserContentReads(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := browserFixtureCommand(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	token := strings.Repeat("d", 64)
	type detailTarget struct {
		native     *sessionapi.Native
		attachment string
		version    sessionpresentation.Version
		workspace  string
	}
	const detailSeq = 64
	details := make(chan detailTarget, 1)
	machine := newWebUIBrowserMachineWithNative(t, ctx, page.URL, "Content", token, func(native *sessionapi.Native, request sessionapi.NativeRequest) bool {
		if request.Operation.Details == nil || request.Operation.Details.Source != detailSeq {
			return false
		}
		details <- detailTarget{native, request.Attachment, request.Version, request.Workspace}
		request.Reply <- ""
		return true
	})
	var imageReads, imageAborts, contentReads, detailReads atomic.Int32
	var holdImage, holdContent, holdDetails, failDetails atomic.Bool
	holdImage.Store(true)
	release := make(chan struct{}, 4)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Method == http.MethodPost && r.URL.Path == "/api/resources/content" {
			// The server notices a canceled browser read only after the
			// request body is consumed.
			var err error
			if body, err = io.ReadAll(r.Body); err != nil {
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if bytes.Contains(body, []byte(`"kind":"details"`)) {
			detailReads.Add(1)
			if holdDetails.Load() {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				case <-ctx.Done():
					return
				}
			}
			if failDetails.Load() {
				// A machine refusal, answered as the connector would.
				w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"code":-32000,"message":"Fixture details unavailable","data":{"status":503}}`)
				return
			}
		} else if r.Method == http.MethodPost && (r.URL.Path == "/api/resources/image" && holdImage.Load() || r.URL.Path == "/api/resources/content" && holdContent.Load()) {
			if body == nil {
				var err error
				if body, err = io.ReadAll(r.Body); err != nil {
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
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
		entries = append(entries, activitypresentation.Entry{Seq: seq, Agent: "Main", Kind: "text", Text: fmt.Sprintf("Earlier step %d.", seq), Revision: 1})
	}
	entries = append(entries,
		activitypresentation.Entry{Seq: 61, Agent: "You", Kind: "text", Text: "Bound image", Images: []string{pixel}, Revision: 1},
		activitypresentation.Entry{Seq: 62, Agent: "Main", Kind: "text", Text: "Status 1", Revision: 1},
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
	// Delay native publications independently of actual resource responses.
	run("eval", `window.heldPublications = []; window.detailConflicts = 0;
		const Original = WebSocket;
		window.WebSocket = class extends Original {
			addEventListener(kind, listener, ...options) {
				if (kind !== 'message') return super.addEventListener(kind, listener, ...options);
				return super.addEventListener(kind, event => {
					if (window.holdPublications && JSON.parse(event.data).method === 'subscription/event') window.heldPublications.push(() => listener.call(this, event));
					else listener.call(this, event);
				}, ...options);
			}
		};
		const originalFetch = window.fetch;
		window.fetch = (...args) => originalFetch(...args).then(response => {
			if (String(args[0]).endsWith('/api/resources/content') && response.status === 409) window.detailConflicts++;
			return response;
		});`)
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
	entries = append(entries, activitypresentation.Entry{Seq: 63, Agent: "Main", Kind: "text", Text: stream + "TAIL_ONE", Revision: 1})
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

	// Oversized details keep their body and reading position while the same
	// selection refreshes, and show a current failure beside that body.
	holdContent.Store(false)
	compaction := activitypresentation.ItemContent{Seq: detailSeq, Agent: "Main", Kind: "compaction", Text: "Detail fixture summary.", Details: &activitypresentation.ItemDetails{CompactionContext: &observation.Context{Known: true, Tokens: 1000, Window: 2000}, CompactionElapsed: time.Second, CompactionTimed: true}}
	entries = append(entries, activitypresentation.Entry{Facts: compaction.Facts, Values: compaction.Values(), Revision: 1})
	publish()
	label := compaction.Values()[0].Label
	run("wait", "--text", label)
	run("find", "role", "button", "click", "--name", label, "--exact")
	var target detailTarget
	select {
	case target = <-details:
	case <-ctx.Done():
		t.Fatal("the details request did not reach the runtime")
	}
	body := func(marker string) string {
		var text strings.Builder
		for line := range 120 {
			fmt.Fprintf(&text, "%s line %03d.\n\n", marker, line)
		}
		return text.String()
	}
	main := sessionpresentation.Thread{ID: "same-thread", Caller: "/root"}
	show := func(source sessionpresentation.Thread, selection, revision uint64, text string) {
		t.Helper()
		target.version.Revision++
		frame := sessionapi.NativeFrame{View: sessionpresentation.View{Version: target.version, Main: main, Input: main, Displayed: main, Info: sessionpresentation.MainInfo{Workspace: target.workspace}}}
		frame.Details = &sessionapi.NativeDetails{Source: source, View: sessionpresentation.DetailView{ID: sessionpresentation.DetailID{Attachment: target.version.Attachment, Selection: selection}, Revision: revision, Pages: []sessionpresentation.DetailItem{{ID: "detail", Kind: "compaction", Label: "Detail fixture", Body: text}}}}
		if err := target.native.Publish(target.attachment, frame); err != nil {
			t.Fatal(err)
		}
	}
	const pane = `document.querySelector('[aria-labelledby="details-heading"] > .overflow-y-auto')`
	panel := func(script string) string {
		t.Helper()
		return strings.TrimSpace(run("eval", fmt.Sprintf(`(() => { const pane = %s; return %s; })()`, pane, script)))
	}
	// The line at the top of the pane, which a refresh must keep in place.
	const anchor = `[...pane.querySelectorAll('p')].find(p => p.getBoundingClientRect().bottom > pane.getBoundingClientRect().top + 1)?.textContent.match(/line (\d+)/)?.[1]`
	reads := func(want int32) {
		t.Helper()
		await(fmt.Sprintf("detail read %d", want), func() bool { return detailReads.Load() == want })
	}
	holdDetails.Store(true)
	show(main, 1, 1, body("FIRST"))
	reads(1)
	run("wait", "--text", "Loading complete details…")
	release <- struct{}{}
	run("wait", "--text", "FIRST line 119.")
	panel(`(pane.scrollTop = pane.scrollHeight / 2, true)`)
	run("wait", "300")
	line, top := panel(anchor), panel(`pane.scrollTop`)
	if line == "" || line == "undefined" || top == "0" {
		t.Fatalf("details did not scroll: line %q, top %s", line, top)
	}
	show(main, 1, 2, body("SECOND"))
	reads(2)
	run("wait", "300")
	if panel(`pane.innerText.includes('FIRST line 119.') && !pane.innerText.includes('Loading complete details')`) != "true" || panel(`pane.scrollTop`) != top || panel(anchor) != line {
		t.Fatal("a same-selection refresh hid the loaded details or moved the reader")
	}
	screenshot("details-refresh-retains-body.png")
	release <- struct{}{}
	run("wait", "--text", "SECOND line 119.")
	if panel(`pane.innerText.includes('FIRST line')`) != "false" || panel(anchor) != line {
		t.Fatal("the replacement details lost the reading position")
	}
	// An unchanged republication keeps the current body without reading again.
	show(main, 1, 2, body("SECOND"))
	run("wait", "300")
	if detailReads.Load() != 2 {
		t.Fatalf("unchanged details republication read again: %d reads", detailReads.Load())
	}
	holdDetails.Store(false)
	failDetails.Store(true)
	show(main, 1, 3, body("THIRD"))
	run("wait", "--text", "Fixture details unavailable")
	if panel(`pane.innerText.includes('SECOND line 119.') && !pane.innerText.includes('THIRD line')`) != "true" {
		t.Fatal("a refresh failure replaced the retained details")
	}
	screenshot("details-refresh-failure.png")
	failDetails.Store(false)
	run("find", "role", "button", "click", "--name", "Retry complete details", "--exact")
	run("wait", "--text", "THIRD line 119.")
	if panel(`pane.innerText.includes('Fixture details unavailable')`) != "false" {
		t.Fatal("a successful retry kept its failure")
	}
	// A stale response can precede the publication that superseded it. It
	// keeps the loaded body without failure chrome or a self-retry.
	holdDetails.Store(true)
	conflicts := run("eval", `window.detailConflicts`)
	staleRead := detailReads.Load()
	show(main, 1, 4, body("STALE"))
	reads(staleRead + 1)
	run("eval", `window.holdPublications = true`)
	show(main, 1, 5, body("CURRENT"))
	run("wait", "--fn", `window.heldPublications.length > 0`)
	release <- struct{}{}
	run("wait", "--fn", fmt.Sprintf(`window.detailConflicts > %s`, conflicts))
	run("wait", "300")
	if panel(`pane.innerText.includes('THIRD line 119.') && !pane.innerText.includes('Retry complete details') && !pane.innerText.includes('The detail selection changed')`) != "true" || detailReads.Load() != staleRead+1 {
		t.Fatal("a superseded detail response showed a failure or retried before publication")
	}
	screenshot("details-conflict-waits.png")
	run("eval", `window.holdPublications = false; window.heldPublications.splice(0).forEach(deliver => deliver())`)
	reads(staleRead + 2)
	release <- struct{}{}
	run("wait", "--text", "CURRENT line 119.")
	// Another source reusing the selection number is a different selection.
	holdDetails.Store(true)
	reused := detailReads.Load()
	show(sessionpresentation.Thread{ID: "same-thread", Caller: "/root/worker"}, 1, 1, body("OTHER"))
	reads(reused + 1)
	run("wait", "--fn", fmt.Sprintf(`(() => { const pane = %s; return !pane.innerText.includes('CURRENT line') && pane.innerText.includes('Loading complete details'); })()`, pane))
	release <- struct{}{}
	run("wait", "--text", "OTHER line 119.")
	show(sessionpresentation.Thread{ID: "same-thread", Caller: "/root/worker"}, 2, 1, body("FOURTH"))
	reads(reused + 2)
	run("wait", "--fn", fmt.Sprintf(`(() => { const pane = %s; return !pane.innerText.includes('OTHER line') && pane.innerText.includes('Loading complete details'); })()`, pane))
	release <- struct{}{}
	run("wait", "--text", "FOURTH line 119.")
	// Inline details replace the hydrated body without a read.
	show(sessionpresentation.Thread{ID: "same-thread", Caller: "/root/worker"}, 2, 2, "INLINE detail body.")
	run("wait", "--text", "INLINE detail body.")
	if panel(`pane.innerText.includes('FOURTH line') || pane.innerText.includes('Loading complete details')`) != "false" || detailReads.Load() != reused+2 {
		t.Fatal("inline details did not replace the hydrated body")
	}
}
