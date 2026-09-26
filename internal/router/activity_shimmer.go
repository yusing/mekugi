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

// terminalColors are the default text and background colors the terminal
// reported through OSC 10 and OSC 11.
type terminalColors struct {
	foreground, background       livediff.RGB
	hasForeground, hasBackground bool
}

// Source: codex-rs/tui/src/summary_shimmer.rs:23:61@86be5320b068ef67b56348b02aa8c33706955da6
// summary_shimmer. Preserve Codex's two-second, whole-grapheme, left-to-right
// cosine sweep: the band takes the terminal foreground and the rest blends
// halfway into the background. Without both reported colors, step through
// dim, normal and bold so the sweep stays within the terminal's own palette.
func reasoningShimmer(text string, elapsed time.Duration, colors terminalColors) string {
	width := float64(ansi.StringWidth(text))
	halfWidth := max(width*.1, 3)
	position := math.Mod(max(0, elapsed.Seconds()), 2)/2*(width+2*halfWidth) - halfWidth
	blend := colors.hasForeground && colors.hasBackground
	var out strings.Builder
	column := 0.0
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		glyph := graphemes.Str()
		glyphWidth := float64(ansi.StringWidth(glyph))
		distance := min(math.Abs(column+glyphWidth/2-position)/halfWidth, 1)
		intensity := .5 * (1 + math.Cos(math.Pi*distance))
		switch {
		case blend:
			alpha := .5 + .5*intensity
			channel := func(fg, bg uint8) int { return int(math.Round(float64(bg) + (float64(fg)-float64(bg))*alpha)) }
			fg, bg := colors.foreground, colors.background
			fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%dm", channel(fg.R, bg.R), channel(fg.G, bg.G), channel(fg.B, bg.B))
		case intensity < .2:
			out.WriteString("\x1b[22;2m")
		case intensity < .6:
			out.WriteString("\x1b[22m")
		default:
			out.WriteString("\x1b[22;1m")
		}
		out.WriteString(glyph)
		column += glyphWidth
	}
	if blend {
		out.WriteString("\x1b[39m")
	} else {
		out.WriteString("\x1b[22m")
	}
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
