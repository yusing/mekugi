package sourcekind

import "strings"

// Format describes the source capabilities for one file path suffix.
type Format struct {
	Kind             string `json:"kind"`
	Language         string `json:"language,omitempty"`
	JSX              bool   `json:"jsx,omitzero"`
	Outline          bool   `json:"outline,omitzero"`
	SemanticResolver string `json:"semanticResolver,omitempty"`
	SyntaxValidation bool   `json:"syntaxValidation,omitzero"`
}

// suffixFormat associates one file path suffix with its source format.
type suffixFormat struct {
	suffix string
	format Format
}

var formats = []suffixFormat{
	{suffix: ".tsx", format: code("typescript", true, "typescript", false)},
	{suffix: ".mts", format: code("typescript", false, "typescript", false)},
	{suffix: ".cts", format: code("typescript", false, "typescript", false)},
	{suffix: ".ts", format: code("typescript", false, "typescript", true)},
	{suffix: ".jsx", format: code("javascript", true, "typescript", false)},
	{suffix: ".mjs", format: code("javascript", false, "typescript", false)},
	{suffix: ".cjs", format: code("javascript", false, "typescript", false)},
	{suffix: ".js", format: code("javascript", false, "typescript", true)},
	{suffix: ".pyi", format: code("python", false, "python", false)},
	{suffix: ".py", format: code("python", false, "python", true)},
	{suffix: ".go", format: code("go", false, "gopls", false)},
	{suffix: ".rs", format: code("rust", false, "", false)},
	{suffix: ".md", format: Format{Kind: "markdown", Outline: true}},
	{suffix: ".json", format: Format{Kind: "json", Outline: true, SemanticResolver: "typescript"}},
}

// code constructs a code-kind source format with the given capabilities.
func code(language string, jsx bool, resolver string, validation bool) Format {
	return Format{
		Kind:             "code",
		Language:         language,
		JSX:              jsx,
		Outline:          true,
		SemanticResolver: resolver,
		SyntaxValidation: validation,
	}
}

// Classify reports the exact case-sensitive source capabilities for path.
func Classify(path string) (Format, bool) {
	for _, candidate := range formats {
		if strings.HasSuffix(path, candidate.suffix) {
			return candidate.format, true
		}
	}
	return Format{}, false
}
