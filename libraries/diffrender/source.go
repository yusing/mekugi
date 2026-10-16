package diffrender

// Source: internal/livediff/render.go@[b95dde39]
import (
	"context"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

// Renderer owns bounded, viewer-local source decoration caches.
type Renderer struct {
	LayoutOnly  bool
	syntax      map[syntaxKey][]string
	lexers      map[string]chroma.Lexer
	syntaxBytes int
}

type syntaxKey struct {
	theme        Theme
	path, source string
}

const maxSyntaxCacheBytes = 8 << 20
const maxSyntaxCacheEntries = 128

// ColorSource uses the filename when supplied, otherwise content analysis.
func (r *Renderer) ColorSource(ctx context.Context, theme Theme, path, source string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := syntaxKey{theme, path, source}
	if lines, ok := r.syntax[key]; ok {
		return lines, nil
	}
	var lexer chroma.Lexer
	matched := false
	lines, err := colorSourceWithMatcher(ctx, theme, path, source, func(path string) chroma.Lexer {
		if path == "" {
			// A detected lexer belongs to this content, not the empty path.
			// The rendered-source cache below also retains negative detections.
			return lexers.Analyse(source)
		}
		var cached bool
		lexer, cached = r.lexers[path]
		if !cached {
			lexer = lexers.Match(path)
		}
		matched = true
		return lexer
	})
	if err != nil || source == "" {
		return lines, err
	}
	// Count string headers as well as content: many blank lines still retain
	// a sizeable slice even though the strings themselves have no bytes.
	size := len(path) + len(source) + len(lines)*2*(strconv.IntSize/8)
	if matched {
		// Conservatively charge each source for its selected path and lexer
		// headers, even when several sources share the same selection.
		size += len(path) + 4*(strconv.IntSize/8)
	}
	for _, line := range lines {
		size += len(line)
	}
	if size > maxSyntaxCacheBytes {
		return lines, nil
	}
	if r.syntaxBytes+size > maxSyntaxCacheBytes || len(r.syntax) >= maxSyntaxCacheEntries {
		clear(r.syntax)
		clear(r.lexers)
		r.syntaxBytes = 0
	}
	if r.syntax == nil {
		r.syntax = make(map[syntaxKey][]string)
	}
	r.syntax[key] = lines
	r.syntaxBytes += size
	if matched {
		if r.lexers == nil {
			r.lexers = make(map[string]chroma.Lexer)
		}
		// Include nil results. Selections live only as long as admitted
		// syntax entries, so unknown paths cannot grow a separate cache.
		r.lexers[path] = lexer
	}
	return lines, nil
}

// WrapSource keeps both syntax foregrounds and word backgrounds on independently
// painted continuation rows. SourceLine keeps gutters and padding at the row fill.
func WrapSource(text string, width int) []string {
	var style uv.Style
	parser := ansi.GetParser()
	defer func() {
		parser.SetHandler(ansi.Handler{})
		ansi.PutParser(parser)
	}()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}})
	var lines []string
	for fragment := range strings.SplitSeq(ansi.Hardwrap(text, max(1, width), true), "\n") {
		carry := ""
		if !style.IsZero() {
			carry = style.String()
		}
		lines = append(lines, carry+fragment)
		parser.Parse([]byte(fragment))
	}
	return lines
}

func SourceLine(theme Theme, width int, numbers, fragment string, kind byte) string {
	style := ""
	if kind == '+' {
		style = theme.Foreground(chroma.GenericInserted)
	} else if kind == '-' {
		style = theme.Foreground(chroma.GenericDeleted)
	}
	line := numbers + style + string(kind) + "\x1b[39m" + fragment
	if background := theme.RowBackground(kind); background != "" {
		// Token resets restore a readable foreground on the fill.
		base := theme.Foreground(chroma.NameOther)
		line = strings.ReplaceAll(line, "\x1b[39m", base)
		line = ansi.Truncate(line, max(0, width-3), "")
		line += background // Word emphasis must not extend into right padding.
		line += strings.Repeat(" ", max(0, width-3-ansi.StringWidth(line)))
		line = background + base + line
	}
	return line + "\x1b[0m"
}

// Tokenise the two sides separately so deleted text cannot change the syntax
// state of additions. Never read the workspace to fill uncaptured source gaps.
func (r *Renderer) ColorHunk(ctx context.Context, theme Theme, review diffmodel.ReviewFile, rows []diffmodel.ReviewRow) ([]string, []string, error) {
	var before, after Output
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		text := Safe(strings.TrimSuffix(row.Text, "\n"), false) + "\n"
		if row.Kind != '+' {
			if _, err := before.WriteString(text); err != nil {
				return nil, nil, err
			}
		}
		if row.Kind != '-' {
			if _, err := after.WriteString(text); err != nil {
				return nil, nil, err
			}
		}
	}
	old, err := r.ColorSource(ctx, theme, review.BeforePath, before.String())
	if err != nil {
		return nil, nil, err
	}
	next, err := r.ColorSource(ctx, theme, review.AfterPath, after.String())
	if err != nil {
		return nil, nil, err
	}
	return colorHunkWords(ctx, theme, rows, old, next)
}

// Syntax is best-effort decoration. Bound lexer input independently of the
// display limit; huge hunks and unknown languages still display exact safe text.
const MaxSyntaxBytes = 256 << 10

func colorSourceWithMatcher(ctx context.Context, theme Theme, path, source string, match func(string) chroma.Lexer) (lines []string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == "" {
		return nil, nil
	}
	plain := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	// Third-party lexers can panic while iterating incomplete source. Syntax
	// decoration must not take down the viewer or expose half-colored content.
	defer func() {
		if recover() != nil {
			lines, err = plain, ctx.Err()
		}
	}()
	if len(source) > MaxSyntaxBytes {
		return plain, nil
	}
	lexer := match(path)
	if lexer == nil {
		return plain, nil
	}
	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return plain, nil
	}
	var commands map[int]string
	if lexer.Config().Name == "Bash" {
		commands = shellCommands(source)
	}
	var output Output
	remaining := source
	for token := iterator(); token != chroma.EOF; token = iterator() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(remaining, token.Value) {
			return plain, nil
		}
		if token.Type.InCategory(chroma.Text) && token.Value != "" && commands[len(source)-len(remaining)] == token.Value {
			token.Type = chroma.NameFunction
		}
		remaining = remaining[len(token.Value):]
		style := theme.Foreground(token.Type)
		// Reset each token fragment so scrolling never inherits another row's
		// style. Only foreground colors are generated, never backgrounds.
		parts := strings.Split(token.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				if _, err := output.WriteString("\n"); err != nil {
					return nil, err
				}
			}
			if part != "" {
				if style != "" {
					part = style + part + "\x1b[39m"
				}
				if _, err := output.WriteString(part); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if remaining != "" {
		return plain, nil
	}
	return strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n"), nil
}
