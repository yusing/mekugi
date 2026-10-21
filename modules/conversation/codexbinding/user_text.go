package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// Source: apps/mekugi/internal/router/live_activity_app_server.go:94:134@45fbe279
// UserText renders userMessage content as the composer wrote it,
// with each image as its "[Image N]" label.
func UserText(content jsontext.Value) (string, []composer.Span, bool) {
	var contents []UserContent
	if len(content) > 0 && json.Unmarshal(content, &contents) != nil {
		return "", nil, false
	}
	var text string
	var spans []composer.Span
	imageCount := 0
	for _, content := range contents {
		if content.Type == "text" {
			if _, attached := attachments.Decode(content.Text); attached {
				continue
			}
			for _, span := range ElementSpans(content.Text, content.Elements) {
				span.Start += len(text)
				span.End += len(text)
				spans = append(spans, span)
			}
			text += content.Text
		} else if content.Type == "image" || content.Type == "localImage" {
			if len(binding.QuestionReplies(text)) > 0 {
				continue // Reply images belong to Asked, not another user-message band.
			}
			start := len(text)
			imageCount++
			text += fmt.Sprintf("[Image %d]", imageCount)
			spans = append(spans, composer.Span{Start: start, End: len(text), Kind: composer.Image})
		}
	}
	draft := conversationmodel.Input{Text: text,
		Skills: SkillBindings(text, ContentSkills(contents), spans)}
	for _, span := range draft.Spans() {
		if !slices.ContainsFunc(spans, func(existing composer.Span) bool {
			return span.Start < existing.End && span.End > existing.Start
		}) {
			spans = append(spans, span)
		}
	}
	slices.SortFunc(spans, func(a, b composer.Span) int { return a.Start - b.Start })
	return text, spans, true
}
