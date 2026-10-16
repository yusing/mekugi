// Source: apps/mekugi/internal/router/app_server_text_spans.go:89:100@[e9da429a] complete skill references.
package composer

import "strings"

func SkillBindings(text, name, path string) []Token {
	var skills []Token
	at := 0
	for word := range strings.FieldsSeq(text) {
		start := at + strings.Index(text[at:], word)
		if word == "$"+name {
			skills = append(skills, Token{Start: start, End: start + len(word), Name: name, Path: path})
		}
		at = start + len(word)
	}
	return skills
}
