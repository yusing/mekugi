package outputbudget

import (
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
	n, err := b.codec.Count(s)
	// Stock execution admits output using ceil(bytes/4), not GPT tokens.
	// Respect both ceilings so a completed selection survives host delivery.
	return max(n, (len(s)+3)/4), err
}
func (b *Budget) Fits(s string, limit int) (bool, error) {
	if len(s) <= limit {
		return true, nil
	}
	if (len(s)+3)/4 > limit {
		return false, nil
	}
	n, e := b.Count(s)
	return n <= limit, e
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
	if len(value) > budget*4 {
		if tail {
			value = value[len(value)-budget*4:]
			for !utf8.ValidString(value) {
				value = value[1:]
			}
		} else {
			value = value[:budget*4]
			for !utf8.ValidString(value) {
				value = value[:len(value)-1]
			}
		}
	}
	_, tokens, err := b.codec.Encode(value)
	if err != nil {
		return Text{}, err
	}
	if len(tokens) <= budget {
		return Text{value, max(len(tokens), (len(value)+3)/4)}, nil
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
