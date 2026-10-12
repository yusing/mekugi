package sourceparse

import (
	"regexp"
	"strings"
)

// IgnoreRule is one pattern from an ignore file, matched against paths
// relative to the directory that holds the file.
type IgnoreRule struct {
	Pattern *regexp.Regexp
	Negate  bool
	DirOnly bool
	// basename rules have no inner slash and match an entry at any depth.
	Basename bool
}

func ParseIgnore(line string) (IgnoreRule, bool) {
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return IgnoreRule{}, false
	}
	// Trailing spaces are dropped unless escaped.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, "\\ ") {
		line = strings.TrimSuffix(line, " ")
	}
	var rule IgnoreRule
	switch {
	case strings.HasPrefix(line, "!"):
		rule.Negate, line = true, line[1:]
	case strings.HasPrefix(line, "\\!"), strings.HasPrefix(line, "\\#"):
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		rule.DirOnly, line = true, strings.TrimRight(line, "/")
	}
	if line == "" {
		return IgnoreRule{}, false
	}
	rule.Basename = !strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	expression, ok := GlobExpression(line)
	if !ok {
		return IgnoreRule{}, false
	}
	pattern, err := regexp.Compile("^" + expression + "$")
	if err != nil {
		return IgnoreRule{}, false
	}
	rule.Pattern = pattern
	return rule, true
}

// GlobExpression translates gitignore glob syntax to a regular
// expression over slash-separated relative paths.
func GlobExpression(pattern string) (string, bool) {
	var expression strings.Builder
	for index := 0; index < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[index:], "**/"):
			expression.WriteString("(?:.*/)?")
			index += 3
		case strings.HasPrefix(pattern[index:], "/**") && index+3 == len(pattern):
			expression.WriteString("/.*")
			index += 3
		case strings.HasPrefix(pattern[index:], "**") && index+2 == len(pattern):
			expression.WriteString(".*")
			index += 2
		case pattern[index] == '*':
			expression.WriteString("[^/]*")
			index++
		case pattern[index] == '?':
			expression.WriteString("[^/]")
			index++
		case pattern[index] == '\\' && index+1 < len(pattern):
			expression.WriteString(regexp.QuoteMeta(pattern[index+1 : index+2]))
			index += 2
		case pattern[index] == '[':
			end := index + 1
			for end < len(pattern) && pattern[end] != ']' {
				// POSIX classes contain their own closing bracket, which is
				// not the end of the enclosing glob bracket expression.
				if strings.HasPrefix(pattern[end:], "[:") {
					close := strings.Index(pattern[end+2:], ":]")
					if close < 0 {
						return "", false
					}
					end += close + 4
				} else {
					end++
				}
			}
			if end == len(pattern) {
				return "", false
			}
			class := pattern[index+1 : end]
			if class == "" {
				return "", false
			}
			if class[0] == '!' {
				class = "^" + class[1:]
			}
			expression.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			index = end + 1
		default:
			expression.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
			index++
		}
	}
	return expression.String(), true
}
