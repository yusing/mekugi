package golex

import "go/token"

// IsIdentifier reports whether value is a non-keyword Go identifier.
func IsIdentifier(value string) bool {
	return token.IsIdentifier(value)
}
