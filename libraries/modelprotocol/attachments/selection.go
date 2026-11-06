package attachments

import (
	"html"
	"strconv"
	"strings"
)

const selectionNotice = "Quoted from the user's screen, not a separate request:\n"

// Selection holds quoted content separately from its visible delimiters.
type Selection struct {
	Label, Source        string
	Header, Body, Footer string
}

// FormatSelection preserves literal content inside an explicit selection boundary.
func FormatSelection(label, source, body string) string {
	header := `<selected-text label="` + html.EscapeString(label) + `"`
	if source != "" {
		header += ` source="` + html.EscapeString(source) + `"`
	}
	header = strings.NewReplacer("\n", "&#10;", "\r", "&#13;", "\t", "&#9;").Replace(header)
	header += ">\n" + selectionNotice
	if diffSelection(label) {
		// The extra newline belongs to framing, not the selected bytes.
		longest, run := 0, 0
		for _, r := range body {
			if r == '`' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("`", max(3, longest+1))
		body = fence + "diff\n" + body + "\n" + fence
	}
	return header + body + "\n</selected-text>"
}

func diffSelection(label string) bool {
	return label == "[Selected diff]" || strings.HasPrefix(label, "[Selected diff ")
}

// ParseSelection reads current markup and historical text headers without
// interpreting quoted contents as markup.
func ParseSelection(frame string) (selection Selection, ok bool) {
	first, rest, found := strings.Cut(frame, "\n")
	if !found {
		return Selection{}, false
	}
	if attrs, current := strings.CutPrefix(first, `<selected-text label="`); current {
		label, attrs, found := strings.Cut(attrs, `"`)
		if !found {
			return Selection{}, false
		}
		selection.Label = html.UnescapeString(label)
		if source, present := strings.CutPrefix(attrs, ` source="`); present {
			source, attrs, found = strings.Cut(source, `"`)
			if !found {
				return Selection{}, false
			}
			selection.Source = html.UnescapeString(source)
		}
		if attrs != ">" {
			return Selection{}, false
		}
		rest, found = strings.CutPrefix(rest, selectionNotice)
		if !found {
			return Selection{}, false
		}
		selection.Footer = "\n</selected-text>"
		if diffSelection(selection.Label) {
			opening, body, found := strings.Cut(rest, "\n")
			fence, valid := strings.CutSuffix(opening, "diff")
			if !found || !valid || len(fence) < 3 || strings.Trim(fence, "`") != "" {
				return Selection{}, false
			}
			rest = body
			selection.Footer = "\n" + fence + selection.Footer
		}
		selection.Header = frame[:len(frame)-len(rest)]
		selection.Body, found = strings.CutSuffix(rest, selection.Footer)
		return selection, found
	}
	header, legacy := strings.CutPrefix(first, SelectionPrefix)
	quoted, err := strconv.QuotedPrefix(header)
	if !legacy || err != nil {
		return Selection{}, false
	}
	selection.Label, _ = strconv.Unquote(quoted)
	header = header[len(quoted):]
	if source, present := strings.CutPrefix(header, " from "); present {
		quoted, err = strconv.QuotedPrefix(source)
		if err != nil {
			return Selection{}, false
		}
		selection.Source, _ = strconv.Unquote(quoted)
		header = source[len(quoted):]
	}
	if !strings.HasPrefix(header, " (UTF-8 bytes ") && header != " (quoted from the user's screen, not a separate request):" {
		return Selection{}, false
	}
	selection.Header, selection.Body = first+"\n", rest
	return selection, true
}
