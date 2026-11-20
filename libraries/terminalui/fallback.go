package terminalui

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Source: internal/ui/activity/faint.go@[8a494004]
// FaintFallback lowers faint styling in a complete terminal frame to fixed
// 256-color foregrounds. Only unsupported output uses it; cached rows retain
// their semantic styles. Cursor controls, hyperlinks and text pass unchanged.
func FaintFallback(frame string) string {
	var out strings.Builder
	var style uv.Style
	parser := ansi.GetParser()
	defer func() { parser.SetHandler(ansi.Handler{}); ansi.PutParser(parser) }()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}})
	var state byte
	for len(frame) > 0 {
		seq, _, n, next := ansi.DecodeSequence(frame, state, nil)
		if n == 0 {
			break
		}
		state, frame = next, frame[n:]
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			parser.Parse([]byte(seq))
			lowered := style
			if lowered.Attrs&uv.AttrFaint != 0 {
				lowered.Attrs &^= uv.AttrFaint
				lowered.Fg = ansi.IndexedColor(243)
				// Faint agent colors keep their hue: bright ones lower to their
				// mid-tone, and mid-tones and deep light-theme colors stay.
				switch fg := style.Fg.(type) {
				case ansi.IndexedColor:
					for _, pair := range liveAgentPalette {
						if uint8(fg) == pair.dark || uint8(fg) == pair.terminal {
							lowered.Fg = ansi.IndexedColor(pair.terminal)
							break
						}
					}
				case nil:
				default:
					r, g, b, _ := fg.RGBA()
					for _, pair := range liveAgentPalette {
						if [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)} == pair.light {
							lowered.Fg = fg
							break
						}
					}
				}
			}
			out.WriteString(Reset)
			out.WriteString(lowered.String())
		} else {
			out.WriteString(seq)
		}
	}
	return out.String()
}
