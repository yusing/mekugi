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
	colors := regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+)m`)
	palettes := []terminalColors{
		{foreground: livediff.RGB{R: 220, G: 214, B: 200}, background: livediff.RGB{R: 24, G: 22, B: 30}, hasForeground: true, hasBackground: true},
		{foreground: livediff.RGB{R: 30, G: 40, B: 50}, background: livediff.RGB{R: 250, G: 248, B: 240}, hasForeground: true, hasBackground: true},
	}
	for _, palette := range palettes {
		fg, bg := palette.foreground, palette.background
		peaks := []int{}
		for _, elapsed := range []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond} {
			frame := reasoningShimmer(text, elapsed, palette)
			if ansi.Strip(frame) != text {
				t.Fatal("shimmer changed summary text")
			}
			peak, strongest := -1, -256
			for i, match := range colors.FindAllStringSubmatch(frame, -1) {
				red, _ := strconv.Atoi(match[1])
				// Every glyph stays between the halfway blend and the terminal foreground.
				low, high := min(int(fg.R), (int(fg.R)+int(bg.R))/2), max(int(fg.R), (int(fg.R)+int(bg.R)+1)/2)
				if red < low || red > high {
					t.Fatalf("glyph color %v leaves the terminal palette %v/%v", match[1:], fg, bg)
				}
				// Closeness to the foreground is the highlight.
				if strength := -max(red-int(fg.R), int(fg.R)-red); strength > strongest {
					peak, strongest = i, strength
				}
			}
			peaks = append(peaks, peak)
		}
		if peaks[0] < 0 || peaks[0] >= len(text)/2 || peaks[1] <= len(text)/2 {
			t.Fatalf("highlight did not sweep left to right: %v", peaks)
		}
		if reasoningShimmer(text, 0, palette) != reasoningShimmer(text, 2*time.Second, palette) {
			t.Fatal("sweep did not repeat after two seconds")
		}
	}
	const unicode = "e\u0301 👩‍💻 你好"
	frame := reasoningShimmer(unicode, time.Second, palettes[0])
	if ansi.Strip(frame) != unicode || !strings.Contains(frame, "e\u0301") || !strings.Contains(frame, "👩‍💻") {
		t.Fatal("shimmer split a grapheme cluster")
	}
}

func TestReasoningShimmerWithoutReportedPalette(t *testing.T) {
	const text = "Checking the answer target"
	frame := reasoningShimmer(text, 500*time.Millisecond, terminalColors{})
	if ansi.Strip(frame) != text || !strings.HasSuffix(frame, "\x1b[39m") {
		t.Fatalf("fallback changed text or leaked styling: %q", frame)
	}
	colors := regexp.MustCompile(`\x1b\[38;2;\d+;\d+;\d+m`)
	levels := map[string]bool{}
	for i := range 31 {
		frame = reasoningShimmer(text, time.Duration(i)*33*time.Millisecond, terminalColors{})
		for _, color := range colors.FindAllString(frame, -1) {
			levels[color] = true
		}
	}
	if len(levels) < 20 {
		t.Fatalf("fallback shimmer has only %d brightness levels", len(levels))
	}

}

func TestNativeUITerminalColorReports(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	for _, key := range []byte("\x1b]10;rgb:dcdc/d6d6/c8c8\x1b\\\x1b]11;rgb:18/16/1e\a") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	want := terminalColors{foreground: livediff.RGB{R: 220, G: 214, B: 200}, background: livediff.RGB{R: 24, G: 22, B: 30}, hasForeground: true, hasBackground: true}
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if view.painter.colors != want || view.painter.theme != livediff.DarkTheme {
			t.Fatalf("pane palette = %+v theme %v", view.painter.colors, view.painter.theme)
		}
	}
	if !u.shell.diff.backgrounded || u.shell.diff.background != want.background || u.shell.diff.theme != livediff.DarkTheme {
		t.Fatal("diff pane did not take the reported background")
	}
	if u.draft != "" {
		t.Fatalf("color reports leaked into the draft: %q", u.draft)
	}
}

func TestAppServerReasoningAnimationLifecycle(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	v := u.agents
	v.apply(activityPaneEvent{Kind: "entries", Agents: []activityPaneAgent{{Name: "/root/reviewer", Responding: true}}, Entries: []activityPaneEntry{{Seq: 1, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", Text: "Checking the answer target", Observed: time.Now().Add(-500 * time.Millisecond)}}})
	first, _ := v.current(v.agents[0], v.entries[0].Observed)
	second, _ := v.current(v.agents[0], v.entries[0].Observed.Add(500*time.Millisecond))
	if !v.hasLiveReasoning() || first == second || ansi.Strip(first) != ansi.Strip(second) {
		t.Fatal("live summary animation froze in roster or changed text")
	}
	before := v.entries[0].Observed
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", Text: "Checking the answer target", Observed: time.Now()}}})
	if v.entries[0].Observed != before {
		t.Fatal("unchanged header restarted animation")
	}
	v.agents[0].Responding = false
	if v.hasLiveReasoning() || !strings.Contains(ansi.Strip(strings.Join(v.renderFeed(80, 20).lines, "\n")), "Checking the answer target") {
		t.Fatal("completed reasoning animation or transcript retention is wrong")
	}
}

func TestStatusPulseReportedPalette(t *testing.T) {
	for _, palette := range []terminalColors{
		{foreground: livediff.RGB{R: 220, G: 220, B: 220}, background: livediff.RGB{R: 20, G: 20, B: 20}, hasForeground: true, hasBackground: true},
		{foreground: livediff.RGB{R: 20, G: 20, B: 20}, background: livediff.RGB{R: 220, G: 220, B: 220}, hasForeground: true, hasBackground: true},
	} {
		start := time.Unix(0, 0)
		dim := statusPulse("Sending…", start, palette)
		bright := statusPulse("Sending…", start.Add(time.Second), palette)
		if dim != "\x1b[38;2;120;120;120mSending…\x1b[39m" || dim != statusPulse("Sending…", start.Add(2*time.Second), palette) {
			t.Fatalf("pulse did not return to midpoint blend: %q", dim)
		}
		want := "\x1b[38;2;220;220;220mSending…\x1b[39m"
		if palette.foreground.R == 20 {
			want = "\x1b[38;2;20;20;20mSending…\x1b[39m"
		}
		if bright != want {
			t.Fatalf("pulse peak = %q, want %q", bright, want)
		}
	}
}

func TestStatusPulseContinuousFallback(t *testing.T) {
	start := time.Unix(0, 0)
	levels := map[string]bool{}
	for i := range 31 {
		frame := statusPulse("Sending…", start.Add(time.Duration(i)*33*time.Millisecond), terminalColors{})
		if ansi.Strip(frame) != "Sending…" {
			t.Fatal("pulse changed text")
		}
		levels[frame] = true
	}
	if len(levels) < 20 {
		t.Fatalf("breathing has only %d levels", len(levels))
	}
}
