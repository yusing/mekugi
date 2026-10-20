package diffrender

// Source: internal/ui/diffview/navigation.go:310:349@[ebde9ad5]
import (
	"fmt"
	"path"
	"strings"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/textsanitize"
)

func statusCode(s diffmodel.Status, theme Theme) (string, string) {
	code := s.ShortCode()
	switch code {
	case "UU", "D":
		return code, theme.Foreground(chroma.GenericDeleted)
	case "A":
		return code, theme.Foreground(chroma.GenericInserted)
	case "RM", "R":
		return code, theme.Accent()
	}
	return code, theme.Foreground(chroma.LiteralNumberInteger)
}

// FileLabel prefixes a file label with its colored status; a rename
// also names its source. Every file row (tree, flat list, Changes tab,
// streaming title) uses this format.
func FileLabel(status diffmodel.Status, label, workspace string, theme Theme) string {
	if status.Directory {
		label = strings.TrimSuffix(label, "/") + "/"
	}
	code, color := statusCode(status, theme)
	label = textsanitize.Safe(label, false)
	if status.Before != "" && status.After != "" && status.Before != status.After {
		// The source's base name when it stayed in the same folder.
		from := pathdisplay.ForWorkspace(workspace, status.Before)
		if path.Dir(status.Before) == path.Dir(status.After) {
			from = path.Base(status.Before)
		}
		label = textsanitize.Safe(from, false) + " → " + label
	}
	return color + code + "\x1b[39m " + label
}

// CountStats shows known line counts; unknown counts are not zero.
func CountStats(count diffmodel.Counts, theme Theme) string {
	if count.Added < 0 || count.Removed < 0 {
		return " " + Subtle + "?" + SubtleReset
	}
	return fmt.Sprintf(" %s+%d\x1b[39m %s-%d\x1b[39m", theme.Foreground(chroma.GenericInserted), count.Added, theme.Foreground(chroma.GenericDeleted), count.Removed)
}
