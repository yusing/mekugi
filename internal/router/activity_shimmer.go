package router

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/livediff"
)

// Source: codex-rs/tui/src/summary_shimmer.rs:23:61@86be5320b068ef67b56348b02aa8c33706955da6
// summary_shimmer. Use Activity's light/dark theme for the neutral brightness
// ramp; preserve Codex's two-second, whole-grapheme, left-to-right cosine sweep.
func reasoningShimmer(text string, elapsed time.Duration, theme livediff.Theme) string {
	width := float64(ansi.StringWidth(text))
	halfWidth := max(width*.1, 3)
	position := math.Mod(max(0, elapsed.Seconds()), 2)/2*(width+2*halfWidth) - halfWidth
	var out strings.Builder
	column := 0.0
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		glyph := graphemes.Str()
		glyphWidth := float64(ansi.StringWidth(glyph))
		distance := min(math.Abs(column+glyphWidth/2-position)/halfWidth, 1)
		intensity := .5 * (1 + math.Cos(math.Pi*distance))
		color := int(math.Round(128 + 127*intensity))
		if theme == livediff.LightTheme {
			color = 255 - color
		}
		fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%dm%s", color, color, color, glyph)
		column += glyphWidth
	}
	out.WriteString("\x1b[39m")
	return out.String()
}

func (v *liveActivityView) hasLiveReasoning() bool {
	if !v.childrenOnly {
		return false
	}
	for _, agent := range v.agents {
		if agent.Responding {
			if i := v.latest(agent.Name); i >= 0 && v.entries[i].Kind == "reasoning" {
				return true
			}
		}
	}
	return false
}
