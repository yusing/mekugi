package router

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

type liveDiffTheme uint8

const (
	liveDiffTerminalTheme liveDiffTheme = iota
	liveDiffLightTheme
	liveDiffDarkTheme
)

// Keep the terminal's background. Chroma owns token inheritance and the paired
// palettes; use only foregrounds, never its diff/error background fills.
func (theme liveDiffTheme) foreground(kind chroma.TokenType) string {
	if kind == chroma.Error || kind.InCategory(chroma.Text) {
		return ""
	}
	if theme != liveDiffTerminalTheme {
		name := "github"
		if theme == liveDiffDarkTheme {
			name = "github-dark"
		}
		// The dark palette otherwise gives built-ins the ordinary name color.
		if kind.InSubCategory(chroma.NameBuiltin) {
			kind = chroma.NameFunction
		}
		color := styles.Get(name).Get(kind).Colour
		if color.IsSet() {
			return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", color.Red(), color.Green(), color.Blue())
		}
		return ""
	}
	// When the background is unknown, use the terminal's own palette rather
	// than assuming dark mode or forcing a black/white source foreground.
	switch {
	case kind == chroma.GenericInserted:
		return "\x1b[32m"
	case kind == chroma.GenericDeleted:
		return "\x1b[31m"
	case kind == chroma.KeywordType,
		kind.InSubCategory(chroma.NameFunction),
		kind.InSubCategory(chroma.NameBuiltin),
		kind == chroma.NameClass, kind == chroma.NameNamespace:
		return "\x1b[34m"
	case kind.InCategory(chroma.Keyword), kind.InCategory(chroma.Operator),
		kind == chroma.NameTag, kind == chroma.NameAttribute, kind == chroma.NameProperty:
		return "\x1b[36m"
	case kind.InSubCategory(chroma.LiteralString):
		return "\x1b[32m"
	case kind.InSubCategory(chroma.LiteralNumber),
		kind == chroma.NameConstant, kind == chroma.NameDecorator:
		return "\x1b[35m"
	case kind.InCategory(chroma.Comment):
		return "\x1b[90m"
	default:
		return ""
	}
}

func (theme liveDiffTheme) accent() string {
	switch theme {
	case liveDiffLightTheme:
		return "\x1b[38;2;0;112;120m"
	case liveDiffDarkTheme:
		return "\x1b[38;2;86;212;221m"
	default:
		return "\x1b[36m"
	}
}

// COLORFGBG is a fallback for terminals that do not answer OSC 11. Its final
// field is the background's palette index; the optional middle field is ignored.
// Only neutral indices have a useful light/dark interpretation without a reply.
func liveDiffEnvironmentTheme(value string) liveDiffTheme {
	fields := strings.Split(value, ";")
	if len(fields) < 2 {
		return liveDiffTerminalTheme
	}
	index, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return liveDiffTerminalTheme
	}
	switch index {
	case 0, 8:
		return liveDiffDarkTheme
	case 7, 15:
		return liveDiffLightTheme
	default:
		return liveDiffTerminalTheme
	}
}

// OSC 11 uses X11 rgb: components of one to four hex digits. Do not treat
// malformed replies as evidence of a dark background.
func liveDiffBackgroundTheme(reply string) (liveDiffTheme, bool) {
	value, ok := strings.CutPrefix(reply, "\x1b]11;rgb:")
	if !ok {
		return liveDiffTerminalTheme, false
	}
	if strings.HasSuffix(value, "\a") {
		value = strings.TrimSuffix(value, "\a")
	} else if strings.HasSuffix(value, "\x1b\\") {
		value = strings.TrimSuffix(value, "\x1b\\")
	} else {
		return liveDiffTerminalTheme, false
	}
	components := strings.Split(value, "/")
	if len(components) != 3 {
		return liveDiffTerminalTheme, false
	}
	var rgb [3]float64
	for i, component := range components {
		if len(component) < 1 || len(component) > 4 ||
			strings.Trim(component, "0123456789abcdefABCDEF") != "" {
			return liveDiffTerminalTheme, false
		}
		n, err := strconv.ParseUint(component, 16, 16)
		if err != nil {
			return liveDiffTerminalTheme, false
		}
		rgb[i] = float64(n) / float64((uint64(1)<<(4*len(component)))-1)
	}
	// Perceived brightness, with green contributing most to readability.
	if .299*rgb[0]+.587*rgb[1]+.114*rgb[2] >= .5 {
		return liveDiffLightTheme, true
	}
	return liveDiffDarkTheme, true
}

// Consume OSC replies separately from navigation. The payload is bounded even
// for malformed/unterminated replies, and an ST terminator may span input reads.
type liveDiffOSC struct {
	active  bool
	escaped bool
	reply   string
}

func (osc *liveDiffOSC) consume(key byte) (reply string, complete bool) {
	if !osc.active {
		osc.active, osc.reply = true, "\x1b]"
		return "", false
	}
	if key == '\a' || osc.escaped && key == '\\' {
		reply = osc.reply + string(key)
		*osc = liveDiffOSC{}
		return reply, true
	}
	osc.escaped = key == 27
	if len(osc.reply) < 128 {
		osc.reply += string(key)
	}
	return "", false
}
