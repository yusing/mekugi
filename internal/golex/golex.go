package golex

import (
	"errors"
	"go/token"
	"strconv"
)

// IsIdentifier reports whether value is a non-keyword Go identifier.
func IsIdentifier(value string) bool {
	return token.IsIdentifier(value)
}

// DecodeStringLiteral decodes one interpreted or raw Go string literal.
func DecodeStringLiteral(literal string) (string, error) {
	if len(literal) < 2 || literal[0] != '"' && literal[0] != '`' {
		return "", errors.New("go string literal must begin with a quote or backtick")
	}
	decoded, err := strconv.Unquote(literal)
	if err != nil {
		return "", err
	}
	return decoded, nil
}
