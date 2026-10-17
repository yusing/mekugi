package codexbinding

import (
	"cmp"
	"encoding/json/jsontext"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// MessageQuestions translates native or retained message facts for the same
// neutral admission path. User text is supplied by the content decoder.
func MessageQuestions(text observation.TextEvent, item observation.Item, userText string, main, history bool) prompt.Message {
	in := prompt.Message{Ref: text.Item.Ref, Kind: text.Kind, Main: main, User: item.Type == "userMessage", History: history, ClientID: item.ClientID}
	if item.Type == "agentMessage" && text.Item.AsyncQuestion {
		questions := make([]binding.Question, len(item.Questions))
		for i, q := range item.Questions {
			questions[i] = binding.Question{ID: q.ID, Header: q.Header, Question: q.Question, Title: q.Title, IsSecret: q.IsSecret, Options: q.Options}
		}
		call := Questions(in.Ref.ThreadID, in.Ref.TurnID, in.Ref.ItemID, nil, questions)
		in.Call = &call
	}
	if in.User && text.Kind == observation.ItemCompleted {
		replies := binding.QuestionReplies(userText)
		in.HasReplies = len(replies) != 0
		for _, r := range replies {
			if item, index, ok := binding.QuestionReplyIdentity(r); ok {
				in.Replies = append(in.Replies, prompt.Reply{Item: item, Index: index, Answer: r.Answer})
			}
		}
	}
	return in
}

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
	var answers []binding.QuestionAnswer
	for i, q := range c.Questions {
		if q.Done && !q.Skipped {
			answer := q.Answer
			if q.IsSecret {
				answer = secretAnswers[i]
			}
			answers = append(answers, binding.QuestionAnswer{ID: q.ID, Answers: answer, Other: q.Selected == len(q.Choices)})
		}
	}
	return binding.EncodeQuestionResponse(answers)
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
