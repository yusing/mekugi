package outputbudget

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

// Source: internal/router/toolplugin/native_reader.go:33:61@[e702dffa] nativeBudget.
type Budget struct{ codec *tokenizer.Codec }

func (b *Budget) Count(s string) (int, error) {
	if b.codec == nil {
		var err error
		b.codec, err = tokenizer.New()
		if err != nil {
			return 0, err
		}
	}
	return b.codec.Count(s)
}
func (b *Budget) Fits(s string, limit int) (bool, error) {
	if len(s) <= limit {
		return true, nil
	}
	n, e := b.Count(s)
	return n <= limit, e
}

func (b *Budget) SelectRows(value string, limit int, tail bool) (string, error) {
	if len(value) <= limit {
		return value, nil
	}
	if _, err := b.Count(""); err != nil {
		return "", err
	}
	return b.codec.SelectRows(value, limit, tail)
}

type Text struct {
	Text   string
	Tokens int
}

// Source: plugins/mrun.ts:17:85@[543de4f3] selectMRunText/formatMRunOutput
func (b *Budget) SelectText(value string, budget int, tail bool) (Text, error) {
	if budget <= 0 {
		return Text{}, nil
	}
	if _, err := b.Count(""); err != nil {
		return Text{}, err
	}
	_, tokens, err := b.codec.Encode(value)
	if err != nil {
		return Text{}, err
	}
	if len(tokens) <= budget {
		return Text{value, len(tokens)}, nil
	}
	for keep := budget; keep > 0; {
		parts := tokens[:keep]
		if tail {
			parts = tokens[len(tokens)-keep:]
		}
		s := strings.Join(parts, "")
		for len(s) > 0 && !utf8.ValidString(s) {
			if tail {
				s = s[1:]
			} else {
				s = s[:len(s)-1]
			}
		}
		n, e := b.Count(s)
		if e != nil {
			return Text{}, e
		}
		if n <= budget {
			return Text{s, n}, nil
		}
		keep -= max(1, n-budget)
	}
	return Text{}, nil
}

type Selection struct{ Stdout, Stderr string }

// Select admits one shared token budget, reserving at most half for stderr
// when stdout is present. Rows mode keeps only complete leading rows.
func Select(ctx context.Context, budget int, mode, stdout, stderr string) (Selection, error) {
	if err := ctx.Err(); err != nil {
		return Selection{}, err
	}
	if budget < 1 || (mode != "head" && mode != "tail" && mode != "rows") {
		return Selection{}, errors.New("invalid output selection")
	}
	counter := Budget{}
	selectValue := func(value string, limit int) (Text, error) {
		s, e := counter.SelectText(value, limit, mode == "tail")
		if e == nil && mode == "rows" && len(s.Text) < len(value) {
			s.Text = s.Text[:strings.LastIndexByte(s.Text, '\n')+1]
			s.Tokens, e = counter.Count(s.Text)
		}
		return s, e
	}
	errorBudget := budget
	if stdout != "" {
		errorBudget = budget / 2
	}
	errorText, err := selectValue(stderr, errorBudget)
	if err != nil {
		return Selection{}, err
	}
	output, err := selectValue(stdout, budget-errorText.Tokens)
	return Selection{Stdout: output.Text, Stderr: errorText.Text}, err
}
