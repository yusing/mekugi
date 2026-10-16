package textsanitize

import "github.com/charmbracelet/x/ansi"

// StripSequences removes terminal escape sequences while preserving field and line
// delimiters. Structured output parsers use it before applying their own grammar;
// displayed fields still require Safe.
func StripSequences(text string) string { return ansi.Strip(text) }
