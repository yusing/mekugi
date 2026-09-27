package activity

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/livediff"
)

// Colors are the default text and background colors the terminal
// reported through OSC 10 and OSC 11.
type Colors struct {
	Foreground, Background       livediff.RGB
	HasForeground, HasBackground bool
}

// Source: codex-rs/tui/src/summary_shimmer.rs:23:61@86be5320b068ef67b56348b02aa8c33706955da6
// summary_shimmer. Preserve Codex's two-second, whole-grapheme, left-to-right
// cosine sweep: the band takes the terminal foreground and the rest blends
// halfway into the background. Unknown palettes use a neutral continuous ramp.
func ReasoningShimmer(text string, elapsed time.Duration, colors Colors) string {
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
		out.WriteString(colors.animationColor(intensity))
		out.WriteString(glyph)
		column += glyphWidth
	}
	out.WriteString("\x1b[39m")
	return out.String()
}

// statusPulse brightens the entire pending label together over a two-second cycle.
func StatusPulse(text string, now time.Time, colors Colors) string {
	phase := float64(now.UnixMilli()%2000) / 2000
	intensity := .5 * (1 - math.Cos(2*math.Pi*phase))
	return colors.animationColor(intensity) + text + "\x1b[39m"
}

// Use continuous RGB levels even before OSC reports arrive, rather than toggling
// font weight. The neutral fallback stays visible on light and dark backgrounds.
func (colors Colors) animationColor(intensity float64) string {
	if !colors.HasForeground || !colors.HasBackground {
		level := int(math.Round(96 + 64*intensity))
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", level, level, level)
	}
	alpha := .5 + .5*intensity
	channel := func(fg, bg uint8) int { return int(math.Round(float64(bg) + (float64(fg)-float64(bg))*alpha)) }
	fg, bg := colors.Foreground, colors.Background
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", channel(fg.R, bg.R), channel(fg.G, bg.G), channel(fg.B, bg.B))
}
