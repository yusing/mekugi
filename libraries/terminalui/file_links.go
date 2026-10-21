package terminalui

import (
	"net/url"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// An OSC hyperlink carries candidates through ANSI wrapping and clipping, but
// this private destination is always removed before terminal delivery.
const fileLinkCandidate = "mekugi-file-candidate:"

// ResolveFileLinks admits only candidates in composed visible rows. Known
// negative results suppress repeated demands just like positive results.
func ResolveFileLinks(row string, known map[string]bool, accent string) (string, []string) {
	if !strings.Contains(row, fileLinkCandidate) {
		return row, nil
	}
	const open = "\x1b]8;;"
	const end = "\x1b\\"
	var out strings.Builder
	var demands []string
	active, linked := false, false
	var style, previous uv.Style
	parser := ansi.GetParser()
	defer func() { parser.SetHandler(ansi.Handler{}); ansi.PutParser(parser) }()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}})
	for {
		before, rest, found := strings.Cut(row, open)
		out.WriteString(before)
		parser.Parse([]byte(before))
		if !found {
			break
		}
		stop := strings.IndexAny(rest, "\x07\x1b")
		if stop < 0 {
			break
		}
		target := rest[:stop]
		terminator := 1
		if strings.HasPrefix(rest[stop:], end) {
			terminator = len(end)
		}
		row = rest[stop+terminator:]
		if encoded, candidate := strings.CutPrefix(target, fileLinkCandidate); candidate {
			active, linked = true, false
			previous = style
			// ANSI clipping retains OSC pairs even when it removes every linked
			// cell. Such empty annotations are not visible demands.
			span, _, _ := strings.Cut(row, open)
			if ansi.StringWidth(span) == 0 {
				continue
			}
			target, err := url.QueryUnescape(encoded)
			if err != nil {
				continue
			}
			allowed, checked := known[target]
			if !checked {
				demands = append(demands, target)
			}
			if allowed {
				if strings.HasPrefix(target, "/") {
					target = (&url.URL{Scheme: "file", Path: target}).String()
				}
				out.WriteString(open + target + end + accent + "\x1b[4m")
				linked = true
			}
		} else if target == "" && active {
			if linked {
				out.WriteString("\x1b[0m" + previous.String() + open + end)
			}
			active, linked = false, false
		} else {
			out.WriteString(open + target + end)
		}
	}
	return out.String(), demands
}
