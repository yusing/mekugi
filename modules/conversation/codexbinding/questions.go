package codexbinding

import (
	"cmp"
	"encoding/json/jsontext"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func MessageQuestions(text observation.TextEvent, item observation.Item, userText string, main, history bool) prompt.Message {
	in := prompt.Message{Ref: text.Item.Ref, Kind: text.Kind, Main: main, User: item.Type == "userMessage", History: history, ClientID: item.ClientID}
	if item.Type == "agentMessage" && text.Item.AsyncQuestion {
		questions := make([]binding.Question, len(item.Questions))
		copy(questions, item.Questions)
		call := Questions(in.Ref.ThreadID, in.Ref.TurnID, in.Ref.ItemID, nil, questions)
		in.Call = &call
	}
	if in.User && text.Kind == observation.ItemCompleted {
		_, in.Replies, in.HasReplies = ReplyText(userText)
	}
	return in
}

func Questions(thread, turn, item string, request jsontext.Value, questions []binding.Question) prompt.Call {
	c := prompt.Call{ID: binding.QuestionIdentity(thread, item, request), Thread: thread, Turn: turn, Item: item, Sync: len(request) > 0}
	for _, wire := range questions {
		q := prompt.Question{ID: wire.ID, Header: cmp.Or(wire.Header, strings.Split(wire.Question, "\n")[0]), Question: wire.Question, IsSecret: wire.IsSecret}
		if !c.Sync && wire.ID == "" {
			q.Outcome = "unavailable"
		}
		for i, o := range wire.Options {
			q.Choices = append(q.Choices, prompt.Option{Label: o.Label, Description: o.Description, Preview: o.Preview, Recommended: o.Recommended})
			if o.Recommended {
				q.Selected = i
			}
		}
		c.Questions = append(c.Questions, q)
	}
	if !c.Sync && len(c.Questions) > 0 {
		c.Resolved = true
		for _, q := range c.Questions {
			if q.Outcome == "" {
				c.Resolved = false
				break
			}
		}
	}
	return c
}

func compactAnswer(q prompt.Question, answer []string) binding.QuestionAnswer {
	a := binding.QuestionAnswer{ID: q.ID}
	switch {
	case !q.Done || q.Skipped:
		a.Skipped = true
	case q.Selected < len(q.Choices):
		a.Choice = q.Selected + 1
		if len(answer) > 1 {
			a.Note = strings.Join(answer[1:], "\n")
		}
	default:
		a.Other = strings.Join(answer, "\n")
	}
	return a
}

func QuestionResponse(c *prompt.Call, secretAnswers map[int][]string) map[string]any {
	answers := make([]binding.QuestionAnswer, 0, len(c.Questions))
	for i, q := range c.Questions {
		answer := q.Answer
		if q.IsSecret {
			answer = secretAnswers[i]
		}
		answers = append(answers, compactAnswer(q, answer))
	}
	return binding.EncodeQuestionResponse(answers)
}

func QuestionReplies(c *prompt.Call) string {
	var replies []binding.QuestionAnswer
	for _, q := range c.Questions {
		if c.Submitting(q) {
			replies = append(replies, compactAnswer(q, q.Answer))
		}
	}
	if len(replies) == 0 {
		return ""
	}
	return binding.EncodeQuestionReplies(replies)
}

func PendingQuestionReply(c *prompt.Call, text string) string {
	var replies []binding.QuestionAnswer
	for _, r := range binding.QuestionReplies(text) {
		for _, q := range c.Questions {
			if q.ID == r.ID && q.Outcome == "" {
				replies = append(replies, compactAnswer(q, q.Answer))
				break
			}
		}
	}
	if len(replies) == 0 {
		return ""
	}
	return binding.EncodeQuestionReplies(replies)
}

func ReplyIDs(text string) []string {
	var ids []string
	for _, reply := range binding.QuestionReplies(text) {
		ids = append(ids, reply.ID)
	}
	return ids
}

func ReplyText(text string, questions ...prompt.Question) (string, []prompt.Reply, bool) {
	replies := binding.QuestionReplies(text)
	var answers []string
	var targets []prompt.Reply
	for _, reply := range replies {
		r := prompt.Reply{ID: reply.ID, Choice: reply.Choice, Note: reply.Note, Other: reply.Other, Skipped: reply.Skipped}
		q := prompt.Question{ID: r.ID}
		for _, question := range questions {
			if question.ID == r.ID {
				q = question
				break
			}
		}
		if answer, ok := r.Readable(q); ok {
			answers = append(answers, answer)
		}
		targets = append(targets, r)
	}
	return strings.Join(answers, "\n"), targets, len(replies) > 0
}
