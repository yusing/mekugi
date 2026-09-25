package router

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// execIgnoreRule is one pattern from an ignore file, matched against paths
// relative to the directory that holds the file.
type execIgnoreRule struct {
	pattern *regexp.Regexp
	negate  bool
	dirOnly bool
	// basename rules have no inner slash and match an entry at any depth.
	basename bool
}

func parseExecIgnoreLine(line string) (execIgnoreRule, bool) {
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return execIgnoreRule{}, false
	}
	// Trailing spaces are dropped unless escaped.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, "\\ ") {
		line = strings.TrimSuffix(line, " ")
	}
	var rule execIgnoreRule
	switch {
	case strings.HasPrefix(line, "!"):
		rule.negate, line = true, line[1:]
	case strings.HasPrefix(line, "\\!"), strings.HasPrefix(line, "\\#"):
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		rule.dirOnly, line = true, strings.TrimRight(line, "/")
	}
	if line == "" {
		return execIgnoreRule{}, false
	}
	rule.basename = !strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	expression, ok := execIgnoreExpression(line)
	if !ok {
		return execIgnoreRule{}, false
	}
	pattern, err := regexp.Compile("^" + expression + "$")
	if err != nil {
		return execIgnoreRule{}, false
	}
	rule.pattern = pattern
	return rule, true
}

// execIgnoreExpression translates gitignore glob syntax to a regular
// expression over slash-separated relative paths.
func execIgnoreExpression(pattern string) (string, bool) {
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
			end := strings.IndexByte(pattern[index+1:], ']')
			if end < 0 {
				return "", false
			}
			class := pattern[index+1 : index+1+end]
			if class == "" {
				return "", false
			}
			if class[0] == '!' {
				class = "^" + class[1:]
			}
			expression.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			index += end + 2
		default:
			expression.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
			index++
		}
	}
	return expression.String(), true
}

// execBuiltinPruned keeps formatter and fixer scopes out of dependency trees,
// bytecode caches, tagged caches, and virtual environments.
func execBuiltinPruned(path, name string) bool {
	if name == "node_modules" || name == "__pycache__" {
		return true
	}
	for _, marker := range []string{"CACHEDIR.TAG", "pyvenv.cfg"} {
		if _, err := os.Lstat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	return false
}
