package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func testCompletionSourcesBrowser(t *testing.T, publish func(*activity.Publication), run func(...string) string) {
	t.Helper()
	capture := func(name string) {
		if directory := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); directory != "" {
			run("screenshot", filepath.Join(directory, "completion-sources-"+name+".png"))
		}
	}
	u := newAppServerSessionTestUI(t, t.TempDir())
	publication := func() *activity.Publication { return u.attached.source.Publication(u.attached.presentation, nil) }
	const prefix = "Completed answer\n"
	completionTestDelta(t, u, prefix)
	publish(publication())
	run("wait", "--text", "Completed answer")
	// Every arriving reference prefix remains ordinary displayed answer text.
	for i := range len(completionTestTrailer) {
		completionTestDelta(t, u, completionTestTrailer[i:i+1])
		current := publication()
		for _, entry := range current.Entries {
			if entry.Text != prefix+completionTestTrailer[:i+1] {
				t.Fatalf("ordinary source text changed in browser publication: %q", entry.Text)
			}
		}
		publish(current)
	}
	completionTestFinish(t, u, prefix+completionTestTrailer)
	publish(publication())
	run("wait", "--text", completionTestTrailer)
	capture("live")
	// Actual parent delivery has a separate reply projection and disclosure.
	publishNativeDelivery(&u.attached, sessioncontrol.NativeDelivery{Thread: "main", ID: "final", Sender: "/root/worker", Recipient: "/root", Kind: "FINAL_ANSWER", Outcome: "completed",
		Message: "## Summary\nDelivered parent answer.\n\n## Details\nRetained details.\n" + completionTestTrailer}, time.Now())
	publish(publication())
	run("wait", "--text", "Delivered parent answer.")
	run("eval", `document.querySelectorAll('[aria-label="Transcript"] details').forEach(e => e.open = true)`)
	run("wait", "--text", "Retained details.")
	if !strings.Contains(publication().Entries[len(publication().Entries)-1].Text, completionTestTrailer) {
		t.Fatal("delivered completion hid its ordinary source reference")
	}
	run("wait", "--text", completionTestTrailer)
	capture("delivered")
	// Restore enough native history to exercise the browser's retained pages.
	u = newAppServerSessionTestUI(t, t.TempDir())
	u.restoreHistory(completionTestHistory(65))
	publish(publication())
	run("wait", "--text", "Restored answer 064")
	// Visible references consume the ordinary page byte budget. Keep reading
	// upward until the oldest restored answer arrives, across as many bounded
	// pages as the consumer needs rather than assuming one earlier-page read.
	run("wait", "--fn", `(() => { const transcript = document.querySelector('[aria-label="Transcript"]'); if (transcript.textContent.includes('Restored answer 000')) return true; const s = transcript.querySelector('.overflow-y-auto'); s.dispatchEvent(new WheelEvent('wheel', {bubbles:true})); s.scrollTop = 0; s.dispatchEvent(new Event('scroll')); return false; })()`)
	run("wait", "--text", completionTestTrailer)
	capture("restored")
	// Replay text uses the same detached publication, including backward seek.
	// This harness publishes Main feeds, not browser replay navigation frames.
	p := completionTestReplay(t)
	for _, at := range []time.Duration{8 * time.Second, 10 * time.Second, 8 * time.Second} {
		if err := p.seek(at); err != nil {
			t.Fatal(err)
		}
		publish(p.ui.source.Publication(p.ui.presentation, nil))
		run("wait", "--text", "Repaint diagnosis")
		if at == 10*time.Second {
			run("wait", "--text", completionTestTrailer)
			capture("replay")
		} else {
			run("wait", "--text", completionTestTrailer[:16])
		}
	}
	u = newAppServerSessionTestUI(t, t.TempDir())
	completionTestFinish(t, u, "Quoted example: `"+completionTestTrailer+"`")
	publish(publication())
	run("wait", "--text", completionTestTrailer)
}
