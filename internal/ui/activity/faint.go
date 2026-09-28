package activity

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

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
				if fg, ok := style.Fg.(ansi.IndexedColor); ok {
					for _, pair := range liveAgentPalette {
						if uint8(fg) == pair.normal {
							lowered.Fg = ansi.IndexedColor(pair.dim)
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
