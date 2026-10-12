package shellsyntax

import (
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

func Literal(word *syntax.Word) (string, bool) {
	if word == nil || !LiteralParts(word.Parts, false) {
		return "", false
	}
	value, err := expand.Literal(&expand.Config{}, word)
	return value, err == nil
}

func LiteralParts(parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch value := part.(type) {
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			if !LiteralParts(value.Parts, true) {
				return false
			}
		case *syntax.Lit:
			if !quoted && strings.ContainsAny(value.Value, "~*?[") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func Heredoc(redirect *syntax.Redirect, partial bool) (string, bool) {
	if (redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc) ||
		(redirect.N != nil && redirect.N.Value != "0") {
		return "", false
	}
	if redirect.Hdoc == nil {
		return "", true
	}
	var source strings.Builder
	for _, part := range redirect.Hdoc.Parts {
		literal, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		source.WriteString(literal.Value)
	}
	content := source.String()
	quoted := false
	for _, part := range redirect.Word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			quoted = true
		case *syntax.Lit:
			quoted = quoted || strings.Contains(part.Value, `\`)
		}
	}
	if !quoted {
		var err error
		content, err = expand.Document(&expand.Config{}, redirect.Hdoc)
		if err != nil {
			return "", false
		}
	}
	if redirect.Op == syntax.DashHdoc {
		lines := strings.Split(content, "\n")
		for index := range lines {
			lines[index] = strings.TrimLeft(lines[index], "\t")
		}
		content = strings.Join(lines, "\n")
	}
	if partial {
		content = strings.TrimSuffix(content, "\n")
	}
	return content, true
}

func Operand(word *syntax.Word) (value string, glob bool, ok bool) {
	if word == nil {
		return "", false, false
	}
	var pattern strings.Builder
	escape := func(text string) {
		for _, char := range text {
			if strings.ContainsRune(`*?[\`, char) {
				pattern.WriteByte('\\')
			}
			pattern.WriteRune(char)
		}
	}
	for index, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false, false
			}
			escape(part.Value)
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				literal, isLiteral := inner.(*syntax.Lit)
				if !isLiteral {
					return "", false, false
				}
				text, err := expand.Literal(&expand.Config{}, &syntax.Word{Parts: []syntax.WordPart{&syntax.DblQuoted{Parts: []syntax.WordPart{literal}}}})
				if err != nil {
					return "", false, false
				}
				escape(text)
			}
		case *syntax.Lit:
			text := part.Value
			if index == 0 && strings.HasPrefix(text, "~") {
				return "", false, false
			}
			if strings.ContainsAny(text, "{}") {
				return "", false, false
			}
			if strings.ContainsAny(text, "*?[") {
				// Character classes and globstar have no filepath.Match
				// form; bash negation is [! where Go uses [^.
				if strings.Contains(text, "[[:") || strings.Contains(text, "**") {
					return "", false, false
				}
				glob = true
				pattern.WriteString(strings.ReplaceAll(text, "[!", "[^"))
				continue
			}
			literal, err := expand.Literal(&expand.Config{}, &syntax.Word{Parts: []syntax.WordPart{part}})
			if err != nil {
				return "", false, false
			}
			escape(literal)
		default:
			return "", false, false
		}
	}
	if glob {
		return pattern.String(), true, true
	}
	literal, err := expand.Literal(&expand.Config{}, word)
	if err != nil {
		return "", false, false
	}
	return literal, false, true
}

func Args(words []*syntax.Word) ([]string, bool) {
	values := make([]string, 0, len(words))
	for _, word := range words {
		value, glob, ok := Operand(word)
		if !ok {
			return nil, false
		}
		if glob {
			// Keep the literal spelling; callers that need operands resolve
			// them through operand().
			value = word.Lit()
			if value == "" {
				return nil, false
			}
		}
		values = append(values, value)
	}
	return values, true
}

func EnvArgs(args []*syntax.Word) ([]*syntax.Word, string) {
	assignments := false
	for len(args) != 0 {
		next, ok := Literal(args[0])
		if !assignments && ok && (next == "-u" || next == "--unset" || strings.HasPrefix(next, "--unset=")) {
			variable, inline := strings.CutPrefix(next, "--unset=")
			count := 1
			if !inline && len(args) > 1 {
				variable, ok = Literal(args[1])
				count = 2
			} else if !inline {
				ok = false
			}
			if !ok || variable == "" || variable == "PATH" {
				return nil, "env unset changes command resolution or is not literal"
			}
			args = args[count:]
			continue
		}
		if !ok || strings.HasPrefix(next, "-") {
			return nil, "env options are not parsed"
		}
		if !strings.Contains(next, "=") {
			break
		}
		if variable, _, _ := strings.Cut(next, "="); variable == "PATH" {
			return nil, "command resolution changes"
		}
		assignments = true
		args = args[1:]
	}
	return args, ""
}

func IsDigits(value string) bool {
	return value != "" && strings.Trim(value, "0123456789") == ""
}

func QuoteArgument(value string) string {
	quoted, err := syntax.Quote(value, syntax.LangBash)
	if err == nil {
		return quoted
	}
	return Quote(value)
}
