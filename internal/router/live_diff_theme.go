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

// Chroma owns token inheritance; row backgrounds are applied separately.
func (theme liveDiffTheme) foreground(kind chroma.TokenType) string {
	if kind == chroma.Error || kind.InCategory(chroma.Text) {
		return ""
	}
	name := "github-dark"
	if theme == liveDiffLightTheme {
		name = "github"
	}
	color := styles.Get(name).Get(kind).Colour
	// Distinguish common categories that GitHub otherwise renders blue or white.
	switch {
	case kind.InSubCategory(chroma.LiteralString):
		color = chroma.MustParseColour("#a5d6a7")
		if theme == liveDiffLightTheme {
			color = chroma.MustParseColour("#0a6634")
		}
	case kind == chroma.KeywordType, kind == chroma.NameClass, kind == chroma.NameNamespace:
		color = chroma.MustParseColour("#80cbc4")
		if theme == liveDiffLightTheme {
			color = chroma.MustParseColour("#006b70")
		}
	case kind.InSubCategory(chroma.NameBuiltin), kind.InSubCategory(chroma.LiteralNumber),
		kind == chroma.NameConstant:
		color = chroma.MustParseColour("#f2c078")
		if theme == liveDiffLightTheme {
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
func (theme liveDiffTheme) rowBackground(kind byte) string {
	if kind != '+' && kind != '-' {
		return ""
	}
	if theme == liveDiffLightTheme {
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
	if before, ok0 := strings.CutSuffix(value, "\a"); ok0 {
		value = before
	} else if before, ok0 := strings.CutSuffix(value, "\x1b\\"); ok0 {
		value = before
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
