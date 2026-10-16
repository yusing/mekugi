package diffrender

// Source: internal/livediff/model.go@[b95dde39]
import (
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

func Gutter(highlighted bool, theme Theme) string {
	if highlighted {
		return theme.Accent() + "▎\x1b[0m "
	}
	return "  "
}

// Give each file a clear section boundary without painting over source colors.
func Header(text string, width int, counts diffmodel.Counts, theme Theme) string {
	width = max(0, width)
	stats := fmt.Sprintf(" %s+%d\x1b[39m %s-%d\x1b[39m", theme.Foreground(chroma.GenericInserted), counts.Added, theme.Foreground(chroma.GenericDeleted), counts.Removed)
	if counts.Added < 0 || counts.Removed < 0 {
		stats = " counts unavailable"
	}
	text = ansi.Truncate(textsanitize.Safe(text, false), max(0, width-ansi.StringWidth(stats)), "")
	header := ansi.Truncate("\x1b[1m"+text+"\x1b[22m"+stats, width, "")
	if remaining := width - ansi.StringWidth(header); remaining > 0 {
		header += Subtle + " " + strings.Repeat("─", remaining-1) + SubtleReset
	}
	return header
}

const MaxSourceBytes = 64 << 20

type Output struct{ strings.Builder }

// WriteString bounds rendered output without per-token byte-slice allocation.
func (b *Output) WriteString(s string) (int, error) {
	if b.Len()+len(s) > MaxSourceBytes {
		return 0, errors.New("rendered diff exceeds 64 MiB")
	}
	return b.Builder.WriteString(s)
}
