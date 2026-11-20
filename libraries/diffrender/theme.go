package diffrender

// Source: internal/livediff/theme.go@[b95dde39]
import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

// Subtle marks secondary text; unsupported terminals receive a fixed-color fallback.
const Subtle = "\x1b[2m"

// SubtleReset clears faint (SGR 22 also clears bold).
const SubtleReset = "\x1b[22m"

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
	if theme == TerminalTheme {
		return terminalForeground(kind)
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

// terminalForeground keeps syntax legible on any background by using the
// terminal's own palette, leaving identifiers and punctuation in its default
// foreground.
func terminalForeground(kind chroma.TokenType) string {
	switch {
	case kind.InCategory(chroma.Comment):
		return "\x1b[90m"
	case kind.InSubCategory(chroma.LiteralString):
		return "\x1b[32m"
	case kind.InSubCategory(chroma.LiteralNumber), kind.InSubCategory(chroma.NameBuiltin), kind == chroma.NameConstant:
		return "\x1b[33m"
	case kind == chroma.KeywordType, kind == chroma.NameClass, kind == chroma.NameNamespace:
		return "\x1b[36m"
	case kind.InCategory(chroma.Keyword), kind == chroma.NameTag:
		return "\x1b[35m"
	case kind == chroma.NameFunction, kind == chroma.NameAttribute:
		return "\x1b[34m"
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

// WordBackground emphasizes changed words without replacing syntax foregrounds.
func (theme Theme) WordBackground(kind byte) string {
	if kind != '+' && kind != '-' {
		return ""
	}
	if theme == LightTheme {
		if kind == '+' {
			return "\x1b[48;2;183;225;191m"
		}
		return "\x1b[48;2;255;190;184m"
	}
	if kind == '+' {
		return "\x1b[48;2;36;76;47m"
	}
	return "\x1b[48;2;100;40;47m"
}

// SelectionBackground is a neutral fill that marks a selected row in place.
func (theme Theme) SelectionBackground() string {
	if theme == LightTheme {
		return "\x1b[48;2;198;212;231m"
	}
	return "\x1b[48;2;62;72;86m"
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

// Semantic foregrounds. The terminal theme, whose background is unknown,
// defers to the terminal's own ANSI palette; dark and light use fixed colors
// legible on their backgrounds.
func (theme Theme) pick(terminal, dark, light string) string {
	switch theme {
	case LightTheme:
		return light
	case DarkTheme:
		return dark
	default:
		return terminal
	}
}

// Success marks completed changes and passing outcomes.
func (theme Theme) Success() string {
	return theme.pick("\x1b[32m", "\x1b[38;5;114m", "\x1b[38;2;26;127;55m")
}

// Warning marks running, pending and waiting states and lasting grants.
func (theme Theme) Warning() string {
	return theme.pick("\x1b[33m", "\x1b[38;5;214m", "\x1b[38;2;154;103;0m")
}

// Danger marks failures and blocked work.
func (theme Theme) Danger() string {
	return theme.pick("\x1b[31m", "\x1b[38;5;203m", "\x1b[38;2;207;34;46m")
}

// Standing marks background work without the urgency of Warning.
func (theme Theme) Standing() string {
	return theme.pick("\x1b[33m", "\x1b[38;5;172m", "\x1b[38;2;134;86;16m")
}

// Info marks reading and inspection.
func (theme Theme) Info() string {
	return theme.pick("\x1b[34m", "\x1b[38;5;75m", "\x1b[38;2;9;105;218m")
}

// Search marks searching and skills.
func (theme Theme) Search() string {
	return theme.pick("\x1b[35m", "\x1b[38;5;141m", "\x1b[38;2;130;80;223m")
}

// Tool marks external tool calls.
func (theme Theme) Tool() string {
	return theme.pick("\x1b[36m", "\x1b[38;5;80m", "\x1b[38;2;0;122;122m")
}

// Hash marks commit identities and staging.
func (theme Theme) Hash() string {
	return theme.pick("\x1b[33m", "\x1b[38;5;179m", "\x1b[38;2;149;56;0m")
}

// HeadingColor follows Bun's terminal Markdown palette: magenta, cyan,
// yellow, green and blue for levels one to five. Level six keeps the default
// foreground, which Bun's white matches only on dark backgrounds.
func (theme Theme) HeadingColor(level int) string {
	if level < 1 || level > 5 {
		return ""
	}
	switch theme {
	case LightTheme:
		return [...]string{"\x1b[38;2;130;80;223m", theme.Accent(), "\x1b[38;2;135;80;0m", "\x1b[38;2;10;102;52m", "\x1b[38;2;9;105;218m"}[level-1]
	case DarkTheme:
		return [...]string{"\x1b[38;2;210;168;255m", theme.Accent(), "\x1b[38;2;242;192;120m", "\x1b[38;2;165;214;167m", "\x1b[38;2;121;192;255m"}[level-1]
	default:
		return [...]string{"\x1b[35m", "\x1b[36m", "\x1b[33m", "\x1b[32m", "\x1b[34m"}[level-1]
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
