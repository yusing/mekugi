// Source: apps/mekugi/internal/router/app_server_text_spans.go:89:100@[e9da429a] complete skill references.
package composer

import (
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

func SkillBindings(text, name, path string) []Token {
	var skills []Token
	for token := range attachments.ReferenceTokens(text) {
		ref, err := attachments.ParseReference(token.Text)
		root, _, _ := strings.Cut(ref.Skill, "/")
		if err == nil && len(ref.Ranges) == 0 && (ref.Skill == name || root == name) {
			skills = append(skills, Token{Start: token.Start, End: token.End, Name: ref.Skill, Path: path})
		}
	}
	return skills
}
