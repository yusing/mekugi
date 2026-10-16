package codexbinding

import (
	"cmp"
	"encoding/json/jsontext"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func Questions(thread, turn, item string, request jsontext.Value, questions []binding.Question) prompt.Call {
	c := prompt.Call{ID: binding.QuestionIdentity(thread, item, request), Thread: thread, Turn: turn, Item: item, Sync: len(request) > 0}
	for _, wire := range questions {
		text := cmp.Or(wire.Question, wire.Title)
		q := prompt.Question{ID: wire.ID, Header: cmp.Or(wire.Header, strings.Split(text, "\n")[0]), Question: text, IsSecret: wire.IsSecret}
		for _, o := range binding.QuestionOptions(wire, c.Sync) {
			q.Choices = append(q.Choices, prompt.Option{Label: o.Label, Description: o.Description})
		}
		c.Questions = append(c.Questions, q)
	}
	return c
}
func QuestionResponse(c *prompt.Call, secretAnswers map[int][]string) map[string]any {
	answers := make(map[string]map[string][]string)
	for i, q := range c.Questions {
		if q.Done && !q.Skipped {
			answer := slices.Clone(q.Answer)
			if q.IsSecret {
				answer = slices.Clone(secretAnswers[i])
			}
			for j := range answer {
				if j > 0 || q.Selected == len(q.Choices) {
					answer[j] = "user_note: " + answer[j]
				}
			}
			answers[q.ID] = map[string][]string{"answers": answer}
		}
	}
	return map[string]any{"answers": answers}
}
func QuestionReplies(c *prompt.Call) string {
	var replies []binding.QuestionReply
	for i := range c.Questions {
		q := &c.Questions[i]
		if q.Outcome != "" {
			continue
		}
		if q.Skipped || !q.Done {
			continue
		}
		replies = append(replies, binding.QuestionReply{Answer: strings.Join(q.Answer, "\n"), Question: q.Question, QuestionItemID: binding.AsyncQuestionIdentity(c.Item, i)})
	}
	if len(replies) == 0 {
		return ""
	}
	return binding.EncodeQuestionReplies(replies)
}

func PendingQuestionReply(c *prompt.Call, text string) string {
	var replies []binding.QuestionReply
	for _, r := range binding.QuestionReplies(text) {
		item, index, ok := binding.QuestionReplyIdentity(r)
		if ok && item == c.Item && index < len(c.Questions) && c.Questions[index].Outcome == "" {
			r.Answer = strings.Join(c.Questions[index].Answer, "\n")
			replies = append(replies, r)
		}
	}
	if len(replies) == 0 {
		return ""
	}
	return binding.EncodeQuestionReplies(replies)
}
