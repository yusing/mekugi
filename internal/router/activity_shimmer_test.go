package router

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestCodexReasoningShimmerSweep(t *testing.T) {
	const text = "Checking the answer target"
	colors := regexp.MustCompile(`\x1b\[38;2;(\d+);\d+;\d+m`)
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		peaks := []int{}
		for _, elapsed := range []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond} {
			frame := reasoningShimmer(text, elapsed, theme)
			if ansi.Strip(frame) != text {
				t.Fatal("shimmer changed summary text")
			}
			peak, strongest := -1, -1
			for i, match := range colors.FindAllStringSubmatch(frame, -1) {
				value, _ := strconv.Atoi(match[1])
				if theme == livediff.LightTheme {
					value = 255 - value
				}
				if value > strongest {
					peak, strongest = i, value
				}
			}
			peaks = append(peaks, peak)
		}
		if peaks[0] < 0 || peaks[0] >= len(text)/2 || peaks[1] <= len(text)/2 {
			t.Fatalf("highlight did not sweep left to right: %v", peaks)
		}
		if reasoningShimmer(text, 0, theme) != reasoningShimmer(text, 2*time.Second, theme) {
			t.Fatal("sweep did not repeat after two seconds")
		}
	}
	const unicode = "e\u0301 👩‍💻 你好"
	frame := reasoningShimmer(unicode, time.Second, livediff.DarkTheme)
	if ansi.Strip(frame) != unicode || !strings.Contains(frame, "e\u0301") || !strings.Contains(frame, "👩‍💻") {
		t.Fatal("shimmer split a grapheme cluster")
	}
}

func TestAppServerReasoningAnimationLifecycle(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	v := u.agents
	v.apply(activityPaneEvent{Kind: "entries", Agents: []activityPaneAgent{{Name: "/root/reviewer", Responding: true}}, Entries: []activityPaneEntry{{Seq: 1, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", Text: "Checking the answer target", Observed: time.Now().Add(-500 * time.Millisecond)}}})
	first := strings.Join(v.renderFeed(80, 20).lines, "\n")
	v.blocks[0][0].observed = time.Now().Add(-1500 * time.Millisecond)
	second := strings.Join(v.renderFeed(80, 20).lines, "\n")
	if !v.hasLiveReasoning() || first == second || ansi.Strip(first) != ansi.Strip(second) {
		t.Fatal("live summary animation froze in cached feed or changed text")
	}
	before := v.entries[0].Observed
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", Text: "Checking the answer target", Observed: time.Now()}}})
	if v.entries[0].Observed != before {
		t.Fatal("unchanged header restarted animation")
	}
	v.agents[0].Responding = false
	if v.hasLiveReasoning() || strings.Contains(ansi.Strip(strings.Join(v.renderFeed(80, 20).lines, "\n")), "Checking the answer target") {
		t.Fatal("completed reasoning kept animating")
	}
}
