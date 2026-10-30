package launch

import (
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"strings"
	"testing"
	"time"
)

func testCompletionMetadataBrowser(t *testing.T, publish func(*activity.Publication), run func(...string) string) {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	publication := func() *activity.Publication { return u.attached.source.Publication(u.attached.presentation, nil) }
	const prefix = "Completed answer\n"
	completionTestDelta(t, u, prefix)
	publish(publication())
	run("wait", "--text", "Completed answer")
	run("eval", `window.protocolFlashed = false; window.protocolObserver = new MutationObserver(() => { const text = document.querySelector('[aria-label="Transcript"]')?.textContent ?? ''; if (text.includes('mekugi-') || text.includes('<!--')) window.protocolFlashed = true; }); window.protocolObserver.observe(document.body, {childList:true, subtree:true, characterData:true});`)
	// Publish every marker prefix. The shared Activity text must be clean even
	// when Markdown would otherwise make the completed HTML comment invisible.
	for i := range len(completionTestTrailer) {
		completionTestDelta(t, u, completionTestTrailer[i:i+1])
		current := publication()
		for _, entry := range current.Entries {
			if entry.Text != strings.TrimSuffix(prefix, "\n") {
				t.Fatalf("partial protocol reached browser publication: %q", entry.Text)
			}
		}
		publish(current)
	}
	completionTestFinish(t, u, prefix+completionTestTrailer)
	publish(publication())
	// Actual parent delivery has a separate reply projection and disclosure.
	publishNativeDelivery(&u.attached, control.NativeInput{Thread: "main", ID: "final", Sender: "/root/worker", Recipient: "/root", Kind: "FINAL_ANSWER", Outcome: control.Completed,
		Message: "## Summary\nDelivered parent answer.\n\n## Details\nRetained details.\n" + completionTestTrailer}, time.Now())
	publish(publication())
	run("wait", "--text", "Delivered parent answer.")
	run("eval", `document.querySelectorAll('[aria-label="Transcript"] details').forEach(e => e.open = true)`)
	run("wait", "--text", "Retained details.")
	if strings.Contains(publication().Entries[len(publication().Entries)-1].Text, "mekugi-reload") || run("eval", `!window.protocolFlashed && !document.querySelector('[aria-label="Transcript"]').textContent.includes('"changes"')`) != "true" {
		t.Fatal("delivered completion exposed metadata in its reply")
	}
	// Restore enough native history to exercise the browser's retained pages.
	u = newAppServerSessionTestUI(t, t.TempDir())
	u.restoreHistory(completionTestHistory(65))
	publish(publication())
	run("wait", "--text", "Restored answer 064")
	run("eval", `const s = document.querySelector('[aria-label="Transcript"] .overflow-y-auto'); s.dispatchEvent(new WheelEvent('wheel', {bubbles:true})); s.scrollTop = 0;`)
	run("wait", "--text", "Restored answer 000")
	if run("eval", `!window.protocolFlashed && !document.querySelector('[aria-label="Transcript"]').textContent.includes('mekugi-reload-v1')`) != "true" {
		t.Fatal("completion framing flashed in live or restored browser history")
	}
	// Replay uses the same detached publication, including backward seek.
	p := completionTestReplay(t)
	for _, at := range []time.Duration{8 * time.Second, 10 * time.Second, 8 * time.Second} {
		if err := p.seek(at); err != nil {
			t.Fatal(err)
		}
		publish(p.ui.source.Publication(p.ui.presentation, nil))
		run("wait", "--text", "Repaint diagnosis")
		if run("eval", `!window.protocolFlashed && !document.querySelector('[aria-label="Transcript"]').textContent.includes('mekugi-')`) != "true" {
			t.Fatal("replay framing reached rendered browser transcript")
		}
	}
	run("eval", `window.protocolObserver.disconnect()`)
	u = newAppServerSessionTestUI(t, t.TempDir())
	completionTestFinish(t, u, "Quoted example: `"+completionTestTrailer+"`")
	publish(publication())
	run("wait", "--fn", `document.querySelector('[aria-label="Transcript"]').textContent.includes('mekugi-reload-v1')`)
}
