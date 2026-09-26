package livediff

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

type Theme uint8

const (
	TerminalTheme Theme = iota
	LightTheme
	DarkTheme
)

// Chroma owns token inheritance; row backgrounds are applied separately.
func (theme Theme) Foreground(kind chroma.TokenType) string {
	if kind == chroma.Error || kind.InCategory(chroma.Text) {
		return ""
	}
	name := "github-dark"
	if theme == LightTheme {
		name = "github"
	}
	color := styles.Get(name).Get(kind).Colour
	// Distinguish common categories that GitHub otherwise renders blue or white.
	switch {
	case kind.InSubCategory(chroma.LiteralString):
		color = chroma.MustParseColour("#a5d6a7")
		if theme == LightTheme {
			color = chroma.MustParseColour("#0a6634")
		}
	case kind == chroma.KeywordType, kind == chroma.NameClass, kind == chroma.NameNamespace:
		color = chroma.MustParseColour("#80cbc4")
		if theme == LightTheme {
			color = chroma.MustParseColour("#006b70")
		}
	case kind.InSubCategory(chroma.NameBuiltin), kind.InSubCategory(chroma.LiteralNumber),
		kind == chroma.NameConstant:
		color = chroma.MustParseColour("#f2c078")
		if theme == LightTheme {
			color = chroma.MustParseColour("#875000")
		}
	}
	if color.IsSet() {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", color.Red(), color.Green(), color.Blue())
	}
	return ""
}

// Changed rows use paired foregrounds and subtle fills, including before theme
// detection completes. Context and chrome retain the terminal background.
func (theme Theme) RowBackground(kind byte) string {
	if kind != '+' && kind != '-' {
		return ""
	}
	if theme == LightTheme {
		if kind == '+' {
			return "\x1b[48;2;230;245;233m"
		}
		return "\x1b[48;2;255;235;233m"
	}
	if kind == '+' {
		return "\x1b[48;2;22;42;29m"
	}
	return "\x1b[48;2;49;27;31m"
}

// SelectionBackground is a neutral fill that marks a selected row in place.
func (theme Theme) SelectionBackground() string {
	if theme == LightTheme {
		return "\x1b[48;2;226;232;240m"
	}
	return "\x1b[48;2;42;48;58m"
}

func (theme Theme) Accent() string {
	switch theme {
	case LightTheme:
		return "\x1b[38;2;0;112;120m"
	case DarkTheme:
		return "\x1b[38;2;86;212;221m"
	default:
		return "\x1b[36m"
	}
}

// COLORFGBG is a fallback for terminals that do not answer OSC 11. Its final
// field is the background's palette index; the optional middle field is ignored.
// Only neutral indices have a useful light/dark interpretation without a reply.
func EnvironmentTheme(value string) Theme {
	fields := strings.Split(value, ";")
	if len(fields) < 2 {
		return TerminalTheme
	}
	index, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return TerminalTheme
	}
	switch index {
	case 0, 8:
		return DarkTheme
	case 7, 15:
		return LightTheme
	default:
		return TerminalTheme
	}
}

// OSC 11 uses X11 rgb: components of one to four hex digits. Do not treat
// malformed replies as evidence of a dark background.
func BackgroundTheme(reply string) (Theme, bool) {
	rgb, ok := colorComponents(reply, "11")
	if !ok {
		return TerminalTheme, false
	}
	// Perceived brightness, with green contributing most to readability.
	if .299*rgb[0]+.587*rgb[1]+.114*rgb[2] >= .5 {
		return LightTheme, true
	}
	return DarkTheme, true
}

// BackgroundColor is the reported background, for blending toward it.
func BackgroundColor(reply string) (RGB, bool) {
	return reportedColor(reply, "11")
}

// ForegroundColor is the reported default text color (an OSC 10 reply).
func ForegroundColor(reply string) (RGB, bool) {
	return reportedColor(reply, "10")
}

func reportedColor(reply, code string) (RGB, bool) {
	rgb, ok := colorComponents(reply, code)
	channel := func(value float64) uint8 { return uint8(value*255 + .5) }
	return RGB{channel(rgb[0]), channel(rgb[1]), channel(rgb[2])}, ok
}

func colorComponents(reply, code string) (rgb [3]float64, ok bool) {
	value, ok := strings.CutPrefix(reply, "\x1b]"+code+";rgb:")
	if !ok {
		return rgb, false
	}
	if before, ok0 := strings.CutSuffix(value, "\a"); ok0 {
		value = before
	} else if before, ok0 := strings.CutSuffix(value, "\x1b\\"); ok0 {
		value = before
	} else {
		return rgb, false
	}
	components := strings.Split(value, "/")
	if len(components) != 3 {
		return rgb, false
	}
	for i, component := range components {
		if len(component) < 1 || len(component) > 4 ||
			strings.Trim(component, "0123456789abcdefABCDEF") != "" {
			return [3]float64{}, false
		}
		n, err := strconv.ParseUint(component, 16, 16)
		if err != nil {
			return [3]float64{}, false
		}
		rgb[i] = float64(n) / float64((uint64(1)<<(4*len(component)))-1)
	}
	return rgb, true
}

// Consume OSC replies separately from navigation. The payload is bounded even
// for malformed/unterminated replies, and an ST terminator may span input reads.
type OSC struct {
	Active  bool
	Escaped bool
	Reply   string
}

func (osc *OSC) Consume(key byte) (reply string, complete bool) {
	if !osc.Active {
		osc.Active, osc.Reply = true, "\x1b]"
		return "", false
	}
	if key == '\a' || osc.Escaped && key == '\\' {
		reply = osc.Reply + string(key)
		*osc = OSC{}
		return reply, true
	}
	osc.Escaped = key == 27
	if len(osc.Reply) < 128 {
		osc.Reply += string(key)
	}
	return "", false
}
