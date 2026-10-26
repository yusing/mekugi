package launch

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
		{Key: "code", CaptureOrder: 1, Origin: changes.Origin{Caller: "/root", Source: "apply_patch"}, Review: file},
		{Key: "plain", CaptureOrder: 2, Origin: changes.Origin{Caller: "/root", Source: "apply_patch"}, Review: unknown},
	}))
	frame := sessionapi.Frame{Session: sessionapi.Session{Thread: "same-thread", Title: "Diffs thread", Workspace: workspace}, Input: &sessionapi.Input{Stoppable: true}, Review: &view}
	publish := func() {
		t.Helper()
		if err := machine.api.Publish(frame); err != nil {
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
			details, _ := exec.CommandContext(ctx, browser, "--session", name, "eval", `document.querySelector('[aria-labelledby="saved-heading"]')?.innerHTML`).CombinedOutput()
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
			}
			for _, theme := range []string{"light", "dark"} {
				run("set", "media", theme)
				run("wait", "--fn", `document.documentElement.scrollWidth <= innerWidth && [...document.querySelectorAll('.diff-word')].some(e => getComputedStyle(e).backgroundColor !== getComputedStyle(e.closest('div.flex')).backgroundColor)`)
				contrast := run("eval", `(() => {
          const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1; const ctx = canvas.getContext('2d');
          const rgba = color => { ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = color; ctx.fillRect(0, 0, 1, 1); const c = [...ctx.getImageData(0, 0, 1, 1).data]; c[3] /= 255; return c; };
          const luminance = c => c.slice(0, 3).map(v => { v /= 255; return v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4; }).reduce((v, c, i) => v + c * [.2126, .7152, .0722][i], 0);
          return Math.min(...[...document.querySelectorAll('span.whitespace-pre span[style]')].filter(e => e.textContent.trim()).map(e => {
            let bg = [255, 255, 255]; const parents = []; for (let p = e; p; p = p.parentElement) parents.push(p);
            for (const p of parents.reverse()) { const c = rgba(getComputedStyle(p).backgroundColor); bg = bg.map((v, i) => c[i] * c[3] + v * (1 - c[3])); }
            const a = luminance(rgba(getComputedStyle(e).color)), b = luminance(bg); return (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
          }));
        })()`)
				if ratio, err := strconv.ParseFloat(strings.TrimSpace(contrast), 64); err != nil || ratio < 4.5 {
					t.Errorf("%s %s %s: syntax contrast must be at least 4.5:1, got %s", state, size.name, theme, contrast)
				}
				if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
					run("screenshot", filepath.Join(directory, state+"-"+size.name+"-"+theme+".png"))
				}
			}
		}
		run("set", "viewport", "1280", "900")
		if state == "saved-diff" {
			openSaved()
		}
	}
	run("open", page.URL)
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
	button("Changes")
	frame.Live = []preview.Preview{{ID: "writing", Caller: "/root", Workspace: workspace, Tool: "apply_patch", Files: []diffmodel.ReviewFile{file}}}
	publish()
	run("wait", "--fn", `document.querySelector('[aria-label="Live edits"] .diff-word[style*="--shiki-light"]')`)
	capture("live-diff")
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
	run("wait", "--fn", `document.querySelector('[aria-labelledby="saved-heading"] .diff-word[style*="--shiki-light"]')`)
	// A retained review can have hundreds of individually short files. Opening
	// its tab must not paint and tokenize every file before accepting input.
	button("Changes")
	var many []changes.Chunk
	for i := range 200 {
		path := filepath.Join(workspace, fmt.Sprintf("retained-%03d.ts", i))
		many = append(many, changes.Chunk{Key: path, CaptureOrder: uint64(i + 1), Origin: changes.Origin{Caller: "/root", Source: "apply_patch"}, Review: diffmodel.RenderReviewFile("", path, "", strings.Repeat("const retained = true;\n", 120))})
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
	run("eval", `document.querySelector('[aria-labelledby="saved-heading"] li:last-child summary').click()`)
	run("wait", "--fn", `document.querySelector('[aria-labelledby="saved-heading"] li:last-child').textContent.includes('const retained = true;')`)
	view.Revision++
	publish()
	run("wait", "--fn", `document.querySelector('[aria-labelledby="saved-heading"] li:last-child details')?.open`)
	t.Log("Saved/live diffs retain source, independent gutters, hunk gaps, syntax, word emphasis, streaming replacement and settling across desktop/tablet/phone in both appearances")
}
