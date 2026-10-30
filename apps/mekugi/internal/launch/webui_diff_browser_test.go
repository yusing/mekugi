package launch

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	"github.com/yusing/mekugi/libraries/diffmodel"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

// Exercise both saved and streaming consumers through the built page, actual
// connector and session server. Fixture owners supply detached edit evidence.
func TestWebUIBrowserDiffs(t *testing.T) {
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
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Diffs", strings.Repeat("d", 64))
	workspace := "/workspace/diff-fixture"
	path := filepath.Join(workspace, "example.ts")
	contextLines := strings.Repeat("// unchanged\n", 12)
	file := diffmodel.RenderReviewFile(path, path, "// previous comment\nconst old_name = 1;\nreturn old_name;\n"+contextLines+"const tail = false;\n", "// updated comment\nconst new_name = 2;\nreturn new_name;\n"+contextLines+"const tail = true;\n")
	unknown := diffmodel.RenderReviewFile("", filepath.Join(workspace, "notes.unknown"), "", "<script>literal & text</script>\n")
	view := changes.ReviewView{Revision: 1, Workspace: workspace}
	view.Publication = view.Publication.With(changes.GroupCaptures([]changes.Chunk{
		{Key: "code", CaptureOrder: 1, Caller: "/root", Source: "apply_patch", Review: file},
		{Key: "plain", CaptureOrder: 2, Caller: "/root", Source: "apply_patch", Review: unknown},
	}))
	frame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Diffs thread", Workspace: workspace}, Input: &sessionapi.Input{Stoppable: true}, Review: &view}
	// Each publication is a detached snapshot: the fixture keeps mutating view
	// and frame.Live, and attachment replays the latest recorded frame.
	publish := func() {
		t.Helper()
		published := frame
		published.Review = new(view)
		published.Live = slices.Clone(frame.Live)
		if err := machine.publishFrame(published); err != nil {
			t.Fatal(err)
		}
	}
	publish()
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-diffs").CombinedOutput()
	if err != nil {
		t.Fatalf("browser identity: %v: %s", err, identity)
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
			snapshot, _ := exec.CommandContext(ctx, browser, "--session", name, "snapshot", "-i").CombinedOutput()
			details, _ := exec.CommandContext(ctx, browser, "--session", name, "eval", `document.querySelector('[aria-labelledby="saved-heading"]')?.innerHTML.slice(0, 4000)`).CombinedOutput()
			t.Fatalf("browser %s: %v: %s\n%s\n%s", args[0], err, out, snapshot, details)
		}
		return string(out)
	}
	button := func(label string) { t.Helper(); run("find", "role", "button", "click", "--name", label, "--exact") }
	openSaved := func() {
		t.Helper()
		if strings.TrimSpace(run("eval", `document.querySelector('button[aria-label="Changes"]')?.getAttribute('aria-pressed') !== 'true'`)) == "true" {
			button("Changes")
		}
	}
	capture := func(state string) {
		t.Helper()
		for _, size := range []struct{ name, width, height string }{{"desktop", "1280", "900"}, {"tablet", "768", "1024"}, {"phone", "390", "844"}} {
			run("set", "viewport", size.width, size.height)
			if state == "saved-diff" {
				openSaved()
			} else if strings.TrimSpace(run("eval", `document.querySelector('button[aria-label="Changes"]')?.getAttribute('aria-pressed') === 'true'`)) == "true" {
				button("Changes")
			}
			if state == "live-grid" {
				run("eval", `document.querySelectorAll('[aria-label="Live edits"] article .overflow-y-auto').forEach(code => code.scrollTop = 0)`)
			}
			for _, theme := range []string{"light", "dark"} {
				run("set", "media", theme)
				run("wait", "--fn", `document.documentElement.scrollWidth <= innerWidth && [...document.querySelectorAll('.diff-word')].some(e => getComputedStyle(e).backgroundColor !== getComputedStyle(e.closest('div.flex')).backgroundColor)`)
				contrast := run("eval", `(() => {
          const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1; const ctx = canvas.getContext('2d');
          const rgba = color => { ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = color; ctx.fillRect(0, 0, 1, 1); const c = [...ctx.getImageData(0, 0, 1, 1).data]; c[3] /= 255; return c; };
          const luminance = c => c.slice(0, 3).map(v => { v /= 255; return v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4; }).reduce((v, c, i) => v + c * [.2126, .7152, .0722][i], 0);
          return Math.min(...[...document.querySelectorAll('span.whitespace-pre span[style], span.whitespace-pre-wrap span[style]')].filter(e => e.textContent.trim()).map(e => {
            let bg = [255, 255, 255]; const parents = []; for (let p = e; p; p = p.parentElement) parents.push(p);
            for (const p of parents.reverse()) { const c = rgba(getComputedStyle(p).backgroundColor); bg = bg.map((v, i) => c[i] * c[3] + v * (1 - c[3])); }
            const a = luminance(rgba(getComputedStyle(e).color)), b = luminance(bg); return (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
          }));
        })()`)
				if ratio, err := strconv.ParseFloat(strings.TrimSpace(contrast), 64); err != nil || ratio < 4.5 {
					t.Errorf("%s %s %s: syntax contrast must be at least 4.5:1, got %s", state, size.name, theme, contrast)
				}
				if strings.HasPrefix(state, "live-") {
					t.Log(size.name, theme, run("eval", `(() => {
            const live = document.querySelector('[aria-label="Live edits"]');
            const cards = [...live.querySelectorAll('article')];
            const boxes = cards.map(e => e.getBoundingClientRect());
            const grid = cards[0].parentElement.getBoundingClientRect();
            const pane = live.getBoundingClientRect();
            const columns = pane.width >= 640 && cards.length > 1 ? 2 : 1;
            if (Math.abs(grid.width - (pane.width - 32)) > 2) throw Error('Live grid does not fill width');
            for (const outer of [live, cards[0].parentElement.parentElement]) {
              outer.scrollTop = 100;
              if (outer.scrollTop || outer.scrollHeight > outer.clientHeight + 1) throw Error('Outer live view scrolls');
            }
            for (let i = 0; i < boxes.length; i++) {
              if (Math.abs(boxes[i].width - (grid.width - (columns - 1) * 12) / columns) > 2) throw Error('Live card does not fill its available columns');
              if (getComputedStyle(cards[i]).borderTopWidth !== '0px') throw Error('Outer card frame remains');
              if (i % columns && Math.abs(boxes[i].top - boxes[i - 1].top) > 1) throw Error('Cards do not share a row');
              if (i >= columns && boxes[i].top < boxes[i - columns].bottom) throw Error('Cards overlap rows');
              if (i % columns && boxes[i].left <= boxes[i - 1].left) throw Error('Cards are not row-major');
              if (boxes[i].bottom > pane.bottom || boxes[i].top < pane.top) throw Error('Card exceeds live viewport');
              const heading = cards[i].querySelector('h3') ?? cards[i].firstElementChild;
              const top = heading.getBoundingClientRect().top;
              if (heading.tagName === 'H3' && (heading.title !== heading.textContent.trim() || heading.getBoundingClientRect().height > parseFloat(getComputedStyle(heading).lineHeight) + 1)) throw Error('Long file header lost its full path or code space');
              if (heading.tagName === 'H3' && heading.title.includes('very-long-component-name-') && heading.lastElementChild.scrollWidth > heading.lastElementChild.clientWidth + 1) throw Error('Directory displaced the distinguishing filename');
              for (const code of cards[i].querySelectorAll('.overflow-y-auto')) {
                const line = code.querySelector('span.whitespace-pre-wrap');
                if (code.clientHeight < parseFloat(getComputedStyle(line).lineHeight)) throw Error('Code cannot display a complete line: ' + JSON.stringify({height:code.clientHeight,line:getComputedStyle(line).lineHeight,heading:heading.getBoundingClientRect().height,whiteSpace:getComputedStyle(heading).whiteSpace,card:boxes[i].height}));
                code.scrollTop = code.scrollHeight;
                if (Math.abs(heading.getBoundingClientRect().top - top) > 1) throw Error('Code scroll moved its header');
              }
            }
            const overflowing = [...live.querySelectorAll('*')].filter(e => !e.closest('h3[title]') && e.clientWidth && e.scrollWidth > e.clientWidth + 1);
            if (overflowing.length) throw Error('Horizontal live overflow: ' + overflowing.map(e => e.tagName + '.' + e.className).join(', '));
            const text = [...live.querySelectorAll('span.whitespace-pre-wrap')].find(e => e.textContent.includes('long_identifier_'));
            if (text && text.getBoundingClientRect().height <= parseFloat(getComputedStyle(text).lineHeight)) throw Error('Long source did not wrap');
            return boxes.map(({x, y, width, height}) => ({x, y, width, height}));
          })()`))
				}
				if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
					run("screenshot", filepath.Join(directory, state+"-"+size.name+"-"+theme+".png"))
				}
			}
		}
		run("set", "viewport", "1280", "900")
		if state == "saved-diff" {
			openSaved()
		} else if strings.TrimSpace(run("eval", `document.querySelector('button[aria-label="Changes"]')?.getAttribute('aria-pressed') === 'true'`)) == "true" {
			button("Changes")
		}
	}
	run("open", page.URL)
	run("eval", `window.heldLivePublications = []; window.controlSockets = []; window.reviewSnapshots = 0; const Original = window.WebSocket; window.WebSocket = class extends Original {
    constructor(...args) { super(...args); window.controlSockets.push(this); }
    addEventListener(kind, listener, ...options) {
      if (kind !== 'message') return super.addEventListener(kind, listener, ...options);
      return super.addEventListener(kind, event => {
        const message = JSON.parse(event.data);
        if (message.method === 'subscription/event' && message.params.kind === 'snapshot' && message.params.data.review) window.reviewSnapshots++;
        if (window.holdLivePublications && message.method === 'subscription/event' && message.params.data.deferred?.live) {
          window.heldLivePublications.push(() => listener.call(this, event)); return;
        }
        listener.call(this, event);
      }, ...options);
    }
  }`)
	run("find", "label", "Machine name", "fill", "Diffs")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", strings.Repeat("d", 64))
	button("Connect")
	run("wait", "--text", "Diffs thread")
	run("find", "text", "Diffs thread", "click", "--exact")
	button("Changes")
	run("wait", "--fn", `document.querySelector('[aria-labelledby="saved-heading"] .diff-word[style*="--shiki-light"]')`)
	run("wait", "--fn", `(() => { const s = document.querySelector('[aria-labelledby="saved-heading"]'); const cells = [...s.querySelectorAll('span.whitespace-pre')]; const plain = cells.find(e => e.textContent === '<script>literal & text</script>'); const added = cells.find(e => e.textContent === 'const new_name = 2;'); return plain && !plain.querySelector('[style], .diff-word, script') && added && [...added.querySelectorAll('.diff-word')].map(e => e.textContent).join('') === 'new_name2' && s.querySelector('details').querySelectorAll('.border-dashed').length === 2 && !s.textContent.includes('@@') && [...s.querySelectorAll('.tabular-nums')].some(e => e.textContent === '15'); })()`)
	capture("saved-diff")
	// Delay each review refresh so the displayed window must survive in flight.
	run("eval", `window.savedFile = document.querySelector('[data-file]'); window.savedDiff = window.savedFile.querySelector('.diff-word'); window.reviewFlashed = false; window.reviewObserver = new MutationObserver(() => { if (document.querySelector('[aria-label="Loading saved changes"]')) window.reviewFlashed = true; }); window.reviewObserver.observe(document.body, {childList:true, subtree:true}); window.pageReads = []; window.fileReads = []; window.originalSend = WebSocket.prototype.send; WebSocket.prototype.send = function(text) { if (JSON.parse(text).method === 'session/page') { window.pageReads.push(() => window.originalSend.call(this, text)); return; } return window.originalSend.call(this, text); }; window.originalFetch = window.fetch; window.fetch = (...args) => String(args[0]).endsWith('/api/resources/content') ? new Promise((resolve, reject) => window.fileReads.push(Object.assign(() => window.originalFetch(...args).then(resolve, reject), {signal: args[1].signal}))) : window.originalFetch(...args)`)
	for i := range 3 {
		if i > 0 {
			run("eval", `document.querySelector('#panel-content > div').scrollTop = 500`)
		}
		view.Revision++
		refreshed := diffmodel.RenderReviewFile(path, path, "const previous = 1;\n", fmt.Sprintf("// refreshed review %d\n", i)+strings.Repeat("const updated = 2;\n", 200))
		view.Publication = view.Publication.With(changes.GroupCaptures([]changes.Chunk{
			{Key: "code", CaptureOrder: 1, Caller: "/root", Source: "apply_patch", Review: refreshed},
		}))
		frame.Session.Title = fmt.Sprintf("Review refresh %d", i)
		publish()
		run("wait", "--text", frame.Session.Title)
		run("wait", "--fn", `window.pageReads.length > 0`)
		if i == 0 {
			// Metadata belongs to the failed control channel and must retry,
			// without making independent content reads reconnect-scoped.
			run("eval", `window.pageReads = []; window.previousReviewSnapshots = window.reviewSnapshots; window.controlSockets.forEach(socket => socket.close())`)
			run("wait", "--fn", `window.reviewSnapshots > window.previousReviewSnapshots && window.pageReads.length === 1`)
		}
		if strings.TrimSpace(run("eval", `window.savedFile.isConnected && window.savedDiff.isConnected && !window.reviewFlashed`)) != "true" {
			t.Fatal("review refresh hid the acquired file or its composed diff")
		}
		if i > 0 {
			run("eval", `(() => { const pane = document.querySelector('#panel-content > div'); pane.scrollTop = 800; window.reviewScroll = pane.scrollTop; window.reviewHeight = pane.scrollHeight; if (window.reviewScroll < 799) throw Error('Review fixture did not scroll inside the long diff'); })()`)
		}
		run("eval", `window.pageReads.shift()()`)
		run("wait", "--fn", `window.fileReads.length > 0`)
		if i == 0 {
			// A control reconnect sends the same review in a fresh snapshot.
			// Its independent HTTP read must finish rather than restart on each
			// snapshot, even when the resource is slower than the control link.
			for range 3 {
				run("eval", `window.previousReviewSnapshots = window.reviewSnapshots; window.controlSockets.forEach(socket => socket.close())`)
				run("wait", "--fn", `window.reviewSnapshots > window.previousReviewSnapshots`)
				run("eval", `if (window.fileReads.length !== 1 || window.fileReads[0].signal.aborted || window.pageReads.length) throw Error('Control reconnect restarted unchanged composed-file content')`)
			}
			t.Log("Three control reconnections retained one pending composed-file request without restarting it")
		}
		if i > 0 {
			run("eval", `(() => { const pane = document.querySelector('#panel-content > div'); if (!window.savedDiff.isConnected || Math.abs(pane.scrollTop - window.reviewScroll) > 1 || Math.abs(pane.scrollHeight - window.reviewHeight) > 2) throw Error('Metadata refresh changed the rendered review: ' + JSON.stringify({connected:window.savedDiff.isConnected,scroll:pane.scrollTop,expectedScroll:window.reviewScroll,height:pane.scrollHeight,expectedHeight:window.reviewHeight})); })()`)
		}
		run("eval", `window.fileReads.shift()()`)
		run("wait", "--text", fmt.Sprintf("// refreshed review %d", i))
		if i > 0 {
			run("eval", `if (Math.abs(document.querySelector('#panel-content > div').scrollTop - window.reviewScroll) > 1) throw Error('Composed file hydration moved the reader')`)
		}
		run("eval", `window.savedDiff = window.savedFile.querySelector('.diff-word'); if (document.querySelector('[aria-labelledby="saved-heading"]').textContent.includes('notes.unknown')) throw Error('Refreshed review retained a removed file')`)
	}
	run("eval", `window.savedFile.querySelector('summary').click()`)
	view.Revision++
	view.Publication = view.Publication.With(changes.GroupCaptures([]changes.Chunk{
		{Key: "code", CaptureOrder: 1, Caller: "/root", Source: "apply_patch", Review: diffmodel.RenderReviewFile(path, path, "const previous = 1;\n", "// closed refresh\n"+strings.Repeat("const updated = 2;\n", 400))},
	}))
	frame.Session.Title = "Closed review refresh"
	publish()
	run("wait", "--text", frame.Session.Title)
	run("wait", "--fn", `window.pageReads.length > 0`)
	run("eval", `window.pageReads.shift()()`)
	run("wait", "--fn", `window.savedFile.querySelector('summary').textContent.includes('+401')`)
	run("eval", `if (window.fileReads.length) throw Error('Closed-file counts required hydration'); window.savedFile.querySelector('summary').click()`)
	run("wait", "--fn", `window.fileReads.length > 0`)
	run("eval", `window.fileReads.shift()()`)
	run("wait", "--text", "// closed refresh")
	run("eval", `WebSocket.prototype.send = window.originalSend; window.fetch = window.originalFetch; window.reviewObserver.disconnect(); if (window.reviewFlashed) throw Error('Saved changes flashed loading during refresh')`)
	button("Changes")
	frame.Live = []preview.Preview{{ID: "writing", Caller: "/root", Workspace: workspace, Tool: "apply_patch", Files: []diffmodel.ReviewFile{file}}}
	publish()
	run("wait", "--fn", `document.querySelector('[aria-label="Live edits"] .diff-word[style*="--shiki-light"]')`)
	capture("live-diff")
	// The content HTTP request can reach the owner after its live revision
	// advances but before the browser receives that revision over WebSocket.
	run("eval", `window.liveReads = []; window.liveResponses = []; window.liveFetch = window.fetch; window.fetch = (...args) => {
    if (!String(args[0]).endsWith('/api/resources/content') || JSON.parse(args[1].body).body.kind !== 'live') return window.liveFetch(...args);
    return new Promise((resolve, reject) => window.liveReads.push(() => window.liveFetch(...args).then(async response => {
      window.liveResponses.push({status:response.status, body:await response.clone().json()}); resolve(response);
    }, reject)));
  }`)
	frame.Live[0].Files = nil
	frame.Live[0].Input = strings.Repeat("streaming source\n", 4000)
	publish()
	run("wait", "--fn", `window.liveReads.length === 1`)
	run("eval", `window.holdLivePublications = true`)
	frame.Live[0].Input += "LATEST LIVE REVISION\n"
	publish()
	run("wait", "--fn", `window.heldLivePublications.length > 0`)
	run("eval", `window.liveReads.shift()()`)
	run("wait", "--fn", `window.liveResponses.length === 1`)
	run("eval", `if (window.liveResponses[0].status !== 409 || window.liveResponses[0].body.message !== 'The referenced section changed.') throw Error('Fixture did not reach section revision rejection')`)
	run("eval", `if (document.body.textContent.includes('The referenced section changed.')) throw Error('Live revision race surfaced as a retry error'); if (window.liveReads.length) throw Error('Obsolete live revision retried')`)
	run("eval", `window.holdLivePublications = false; window.heldLivePublications.splice(0).forEach(deliver => deliver())`)
	run("wait", "--fn", `window.liveReads.length === 1`)
	run("eval", `window.liveReads.shift()()`)
	run("wait", "--fn", `document.querySelector('[aria-label="Live edits"]')?.textContent.includes('LATEST LIVE REVISION')`)
	run("eval", `if (window.liveResponses.length !== 2 || window.liveResponses[1].status !== 200 || document.body.textContent.includes('The referenced section changed.')) throw Error('Live revision did not recover automatically'); window.fetch = window.liveFetch`)
	frame.Live[0].Input = ""
	// Arrival order fills a row before starting the next, for file diffs,
	// unparsed input and streamed unified-diff fallback alike.
	long := strings.Repeat("long_identifier_", 24)
	frame.Live[0].Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "const old = 1;\n", "const new = '"+long+"';\n"+strings.Repeat("// more source\n", 80))}
	for i := 1; i < 4; i++ {
		frame.Live = append(frame.Live, preview.Preview{ID: fmt.Sprint(i), Caller: "/root", Workspace: workspace, Tool: "apply_patch", Input: long})
		if i == 2 {
			frame.Live[i].DiffText = true
			frame.Live[i].Input = "--- a/raw.ts\n+++ b/raw.ts\n@@ -1 +1 @@\n-old\n+" + long + "\n"
		}
		publish()
		run("wait", "--fn", fmt.Sprintf(`document.querySelectorAll('[aria-label="Live edits"] article').length === %d`, i+1))
	}
	capture("live-grid")
	// Readers who scroll up keep their place; returning to the end resumes
	// following streamed growth in that code block only.
	run("eval", `window.liveCodes = [...document.querySelectorAll('[aria-label="Live edits"] article .overflow-y-auto')]; window.liveCodes[0].scrollTop = 0; window.liveCodes[3].scrollTop = 0`)
	frame.Live[3].Input += strings.Repeat("\nmore source", 80)
	publish()
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Live edits"] pre:last-child span.flex').length > 80`)
	run("eval", `new Promise(resolve => requestAnimationFrame(() => { window.liveCodes[3].scrollTop = 0; window.readerTop = 0; requestAnimationFrame(resolve); }))`)
	frame.Live[3].Input += "\nwhile reading"
	publish()
	run("wait", "--text", "while reading")
	run("eval", `if (window.liveCodes[3].scrollTop !== window.readerTop) throw Error('Live growth moved the reader'); window.liveCodes[3].scrollTop = window.liveCodes[3].scrollHeight`)
	run("wait", "--fn", `window.liveCodes[3].scrollTop > 0`)
	frame.Live[3].Input += strings.Repeat("\nfollow source", 40)
	publish()
	run("wait", "--fn", `(() => { const p = window.liveCodes[3]; return p.textContent.includes('follow source') && p.scrollHeight - p.scrollTop - p.clientHeight < 2 && window.liveCodes[0].scrollTop === 0; })()`)
	frame.Live[3].DiffText = true
	frame.Live[3].Input = "--- a/fallback.ts\n+++ b/fallback.ts\n@@ -1 +1 @@\n-old\n" + strings.Repeat("+streamed fallback\n", 100)
	publish()
	run("wait", "--text", "streamed fallback")
	frame.Live[3].Input += strings.Repeat("+continued fallback\n", 100)
	publish()
	run("wait", "--text", "continued fallback")
	run("wait", "--fn", `(() => { const p = window.liveCodes[3]; return p.textContent.includes('continued fallback') && p.scrollHeight - p.scrollTop - p.clientHeight < 2; })()`)
	for i := range frame.Live {
		longPath := filepath.Join(workspace, "apps/mekugi/internal/webui/app/src/lib/components/session", fmt.Sprintf("very-long-component-name-%d.svelte", i))
		frame.Live[i].Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile(longPath, longPath, "const old_name = 1;\n", "const new_name = 2;\n"+strings.Repeat("// readable code\n", 80))}
	}
	publish()
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Live edits"] h3').length === 4`)
	capture("live-files")
	button("Show transcript")
	run("wait", "--fn", `!document.querySelector('[aria-label="Live edits"]')`)
	frame.Live = frame.Live[:1]
	frame.Live[0].ID = "writing-return"
	publish()
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Live edits"] article').length === 1`)
	// Multiple files in one call keep each path above its own scroll area.
	frame.Live[0].Files = append(frame.Live[0].Files, diffmodel.RenderReviewFile("", filepath.Join(workspace, "second.ts"), "", strings.Repeat("const second = 1;\n", 80)))
	publish()
	run("wait", "--fn", `document.querySelectorAll('[aria-label="Live edits"] h3').length === 2`)
	run("eval", `(() => { const card = document.querySelector('[aria-label="Live edits"] article'); const headings = [...card.querySelectorAll('h3')]; const codes = [...card.querySelectorAll('.overflow-y-auto')]; const tops = headings.map(h => h.getBoundingClientRect().top); codes[1].scrollTop = 0; codes[0].scrollTop = codes[0].scrollHeight; if (!codes[0].scrollTop || codes[1].scrollTop || headings.some((h, i) => h.getBoundingClientRect().top !== tops[i])) throw Error('File scroll areas are not independent'); })()`)
	// Replacing a streaming snapshot must remove previous decorations and keep
	// the new syntax colors and word emphasis on the same source characters.
	frame.Live[0].Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "const old = 1;\n", "const latest = 3;\n")}
	publish()
	run("wait", "--fn", `(() => { const s = document.querySelector('[aria-label="Live edits"]'); return s && s.textContent.includes('latest') && !s.textContent.includes('new_name') && [...s.querySelectorAll('.diff-word[style*="--shiki-light"]')].some(e => e.textContent === 'latest'); })()`)
	// Small generated arrays stay syntax-highlighted when repeated tokens use
	// the matching-work budget; the next ordinary update still emphasizes words.
	repeated := "const xs = [" + strings.Repeat("0, ", 330)
	frame.Live[0].Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, repeated+"1];\n", repeated+"2];\n")}
	publish()
	run("wait", "--fn", `(() => { const s = document.querySelector('[aria-label="Live edits"]'); return s && s.textContent.includes('const xs') && s.querySelector('[style*="--shiki-light"]') && !s.querySelector('.diff-word'); })()`)
	frame.Live[0].Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "const old = 1;\n", "const latest = 3;\n")}
	publish()
	run("wait", "--fn", `document.querySelector('[aria-label="Live edits"] .diff-word[style*="--shiki-light"]')`)
	frame.Live[0].Complete = true
	publish()
	run("wait", "--fn", `!document.querySelector('[aria-label="Live edits"]')`)
	openSaved()
	run("wait", "--text", "// closed refresh")
	// A retained review can have hundreds of individually short files. Opening
	// its tab must not paint and tokenize every file before accepting input.
	button("Changes")
	var many []changes.Chunk
	for i := range 200 {
		path := filepath.Join(workspace, fmt.Sprintf("retained-%03d.ts", i))
		many = append(many, changes.Chunk{Key: path, CaptureOrder: uint64(i + 1), Caller: "/root", Source: "apply_patch", Review: diffmodel.RenderReviewFile("", path, "", strings.Repeat("const retained = true;\n", 120))})
	}
	view.Revision++
	view.Publication = changes.Publication{}.With(changes.GroupCaptures(many))
	frame.Live = nil
	publish()
	run("wait", "--fn", `document.querySelector('button[aria-label="Changes"]')?.textContent.includes('24000')`)
	t.Log("large review first paint:", run("eval", `new Promise(resolve => { const start = performance.now(); document.querySelector('button[aria-label="Changes"]').click(); requestAnimationFrame(() => requestAnimationFrame(() => resolve({ms: performance.now() - start, rows: document.querySelectorAll('[aria-labelledby="saved-heading"] span.whitespace-pre').length, files: document.querySelectorAll('[aria-labelledby="saved-heading"] details').length}))); })`))
	if strings.TrimSpace(run("eval", `document.querySelectorAll('[aria-labelledby="saved-heading"] span.whitespace-pre').length <= 300`)) != "true" {
		t.Fatal("opening a large saved review rendered more than the initial line budget")
	}
	for range 10 {
		if strings.TrimSpace(run("eval", `document.querySelectorAll('[aria-labelledby="saved-heading"] details').length === 200`)) == "true" {
			break
		}
		run("eval", `(() => { const list = document.querySelector('#panel-content > div'); window.fileCount = list.querySelectorAll('details').length; list.scrollTop = list.scrollHeight; })()`)
		run("wait", "--fn", `document.querySelector('#panel-content').querySelectorAll('details').length > window.fileCount`)
	}
	if strings.TrimSpace(run("eval", `document.querySelectorAll('[aria-labelledby="saved-heading"] details').length === 200 && !document.body.innerText.includes('Load more')`)) != "true" {
		t.Fatal("saved files did not page automatically")
	}
	run("eval", `document.querySelector('[aria-labelledby="saved-heading"] li:last-child summary').click()`)
	run("wait", "--fn", `document.querySelector('[aria-labelledby="saved-heading"] li:last-child').textContent.includes('const retained = true;')`)
	run("eval", `(() => { const pane = document.querySelector('#panel-content > div'); const row = [...pane.querySelectorAll('[data-file]')].find(node => node.getBoundingClientRect().bottom > pane.getBoundingClientRect().top); window.reviewAnchor = row.dataset.file; window.reviewOffset = row.getBoundingClientRect().top - pane.getBoundingClientRect().top; const summary = pane.querySelector('li:last-child summary'); summary.focus({preventScroll:true}); window.reviewFocus = summary.closest('[data-file]').dataset.file; })()`)
	view.Revision++
	publish()
	run("wait", "--fn", `document.querySelector('[aria-labelledby="saved-heading"] li:last-child details')?.open`)
	run("wait", "--fn", `[...document.querySelectorAll('[aria-labelledby="saved-heading"] details[open]')].every(node => node.textContent.includes('const retained = true;'))`)
	if strings.TrimSpace(run("eval", `(() => { const pane = document.querySelector('#panel-content > div'); const row = [...pane.querySelectorAll('[data-file]')].find(node => node.dataset.file === window.reviewAnchor); return Math.abs(row.getBoundingClientRect().top - pane.getBoundingClientRect().top - window.reviewOffset) < 2 && document.activeElement.closest('[data-file]')?.dataset.file === window.reviewFocus; })()`)) != "true" {
		t.Fatal("review refresh lost its later-page reading anchor or focus")
	}
	// A metadata failure must not hydrate newly opened files from old references.
	run("eval", `window.failedContentReads = []; window.failReviewPage = true; window.originalSend = WebSocket.prototype.send; WebSocket.prototype.send = function(text) { const message = JSON.parse(text); if (message.method === 'session/page' && window.failReviewPage) { window.failReview = () => this.dispatchEvent(new MessageEvent('message', {data: JSON.stringify({jsonrpc:'2.0',id:message.id,error:{code:-32000,message:'Fixture review refresh failed'}})})); return; } return window.originalSend.call(this,text); }; window.originalFetch = window.fetch; window.fetch = (...args) => String(args[0]).endsWith('/api/resources/content') ? new Promise((resolve, reject) => window.failedContentReads.push({reference:JSON.parse(args[1].body).body,send:() => window.originalFetch(...args).then(resolve, reject)})) : window.originalFetch(...args)`)
	view.Revision++
	frame.Session.Title = "Review retry fixture"
	publish()
	run("wait", "--text", frame.Session.Title)
	run("wait", "--fn", `!!window.failReview`)
	run("eval", `document.querySelectorAll('[aria-labelledby="saved-heading"] li')[3].querySelector('summary').click(); window.failReview()`)
	run("wait", "--text", "Fixture review refresh failed")
	run("eval", `if (window.failedContentReads.length) throw Error('Metadata failure started hydration from an old revision'); window.failReviewPage = false; [...document.querySelectorAll('[aria-labelledby="saved-heading"] button')].find(button => button.textContent.trim() === 'Retry').click()`)
	run("wait", "--fn", `window.failedContentReads.length > 0`)
	run("eval", fmt.Sprintf(`if (window.failedContentReads.some(read => read.reference.revision !== %d)) throw Error('Retry used obsolete content references'); window.failedContentReads.splice(0).forEach(read => read.send()); WebSocket.prototype.send = window.originalSend; window.fetch = window.originalFetch`, view.Revision))
	run("wait", "--fn", `document.querySelectorAll('[aria-labelledby="saved-heading"] li')[3].textContent.includes('const retained = true;')`)
	t.Log("Saved/live diffs retain source, independent gutters, hunk gaps, syntax, word emphasis, streaming replacement and settling across desktop/tablet/phone in both appearances")
}
