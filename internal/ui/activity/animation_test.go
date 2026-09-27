package activity_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestCodexReasoningShimmerSweep(t *testing.T) {
	const text = "Checking the answer target"
	colors := regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+)m`)
	palettes := []activityui.Colors{
		{Foreground: livediff.RGB{R: 220, G: 214, B: 200}, Background: livediff.RGB{R: 24, G: 22, B: 30}, HasForeground: true, HasBackground: true},
		{Foreground: livediff.RGB{R: 30, G: 40, B: 50}, Background: livediff.RGB{R: 250, G: 248, B: 240}, HasForeground: true, HasBackground: true},
	}
	for _, palette := range palettes {
		fg, bg := palette.Foreground, palette.Background
		peaks := []int{}
		for _, elapsed := range []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond} {
			frame := activityui.ReasoningShimmer(text, elapsed, palette)
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
		if activityui.ReasoningShimmer(text, 0, palette) != activityui.ReasoningShimmer(text, 2*time.Second, palette) {
			t.Fatal("sweep did not repeat after two seconds")
		}
	}
	const unicode = "e\u0301 👩‍💻 你好"
	frame := activityui.ReasoningShimmer(unicode, time.Second, palettes[0])
	if ansi.Strip(frame) != unicode || !strings.Contains(frame, "e\u0301") || !strings.Contains(frame, "👩‍💻") {
		t.Fatal("shimmer split a grapheme cluster")
	}
}

func TestReasoningShimmerWithoutReportedPalette(t *testing.T) {
	const text = "Checking the answer target"
	frame := activityui.ReasoningShimmer(text, 500*time.Millisecond, activityui.Colors{})
	if ansi.Strip(frame) != text || !strings.HasSuffix(frame, "\x1b[39m") {
		t.Fatalf("fallback changed text or leaked styling: %q", frame)
	}
	colors := regexp.MustCompile(`\x1b\[38;2;\d+;\d+;\d+m`)
	levels := map[string]bool{}
	for i := range 31 {
		frame = activityui.ReasoningShimmer(text, time.Duration(i)*33*time.Millisecond, activityui.Colors{})
		for _, color := range colors.FindAllString(frame, -1) {
			levels[color] = true
		}
	}
	if len(levels) < 20 {
		t.Fatalf("fallback shimmer has only %d brightness levels", len(levels))
	}

}

func TestStatusPulseReportedPalette(t *testing.T) {
	for _, palette := range []activityui.Colors{
		{Foreground: livediff.RGB{R: 220, G: 220, B: 220}, Background: livediff.RGB{R: 20, G: 20, B: 20}, HasForeground: true, HasBackground: true},
		{Foreground: livediff.RGB{R: 20, G: 20, B: 20}, Background: livediff.RGB{R: 220, G: 220, B: 220}, HasForeground: true, HasBackground: true},
	} {
		start := time.Unix(0, 0)
		dim := activityui.StatusPulse("Sending…", start, palette)
		bright := activityui.StatusPulse("Sending…", start.Add(time.Second), palette)
		if dim != "\x1b[38;2;120;120;120mSending…\x1b[39m" || dim != activityui.StatusPulse("Sending…", start.Add(2*time.Second), palette) {
			t.Fatalf("pulse did not return to midpoint blend: %q", dim)
		}
		want := "\x1b[38;2;220;220;220mSending…\x1b[39m"
		if palette.Foreground.R == 20 {
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
		frame := activityui.StatusPulse("Sending…", start.Add(time.Duration(i)*33*time.Millisecond), activityui.Colors{})
		if ansi.Strip(frame) != "Sending…" {
			t.Fatal("pulse changed text")
		}
		levels[frame] = true
	}
	if len(levels) < 20 {
		t.Fatalf("breathing has only %d levels", len(levels))
	}
}
