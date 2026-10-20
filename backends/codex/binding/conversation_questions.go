// Source: apps/mekugi/internal/router/app_server_questions.go:25:42,593:655@[42afca9c] Codex question protocol.
package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"strings"
)

// QuestionAnswer contains completed input only. Secret answers may pass through
// this response encoder but never enter the feature's retained answer records.
type QuestionAnswer struct {
	ID      string
	Answers []string
	Other   bool
}

func EncodeQuestionResponse(completed []QuestionAnswer) map[string]any {
	answers := make(map[string]map[string][]string)
	for _, q := range completed {
		answer := slices.Clone(q.Answers)
		for i := range answer {
			if i > 0 || q.Other {
				answer[i] = "user_note: " + answer[i]
			}
		}
		answers[q.ID] = map[string][]string{"answers": answer}
	}
	return map[string]any{"answers": answers}
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}
type Question struct {
	ID       string         `json:"id"`
	Header   string         `json:"header"`
	Question string         `json:"question"`
	Title    string         `json:"title"`
	IsSecret bool           `json:"isSecret"`
	Options  jsontext.Value `json:"options"`
}

type QuestionRequest struct {
	ThreadID  string     `json:"threadId"`
	TurnID    string     `json:"turnId"`
	ItemID    string     `json:"itemId"`
	Questions []Question `json:"questions"`
}

func DecodeQuestionRequest(params jsontext.Value) (QuestionRequest, error) {
	var p QuestionRequest
	err := json.Unmarshal(params, &p)
	return p, err
}

type ResolvedRequest struct {
	ThreadID  string         `json:"threadId"`
	RequestID jsontext.Value `json:"requestId"`
}

func DecodeResolvedRequest(params jsontext.Value) (ResolvedRequest, error) {
	var p ResolvedRequest
	err := json.Unmarshal(params, &p)
	return p, err
}
func QuestionOptions(q Question, sync bool) []QuestionOption {
	var options []QuestionOption
	if sync {
		_ = json.Unmarshal(q.Options, &options)
	} else {
		var labels []string
		_ = json.Unmarshal(q.Options, &labels)
		for _, label := range labels {
			options = append(options, QuestionOption{Label: label})
		}
	}
	return options
}
func QuestionIdentity(thread, item string, request jsontext.Value) string {
	if len(request) > 0 {
		return thread + "\x00request/" + string(request)
	}
	return thread + "\x00item/" + item
}
func EncodeQuestionReplies(replies []QuestionReply) string {
	data, _ := json.Marshal(replies)
	return QuestionReplyStart + string(data) + QuestionReplyEnd
}
func AsyncQuestionIdentity(item string, index int) string {
	data, _ := json.Marshal([]any{"request_user_input_async", item, index})
	return string(data)
}

const QuestionReplyStart = "<send_user_message_question_reply>"
const QuestionReplyEnd = "</send_user_message_question_reply>"

type QuestionReply struct {
	Answer         string `json:"answer"`
	Question       string `json:"question"`
	QuestionItemID string `json:"questionItemId"`
}

// Match the whole stock payload, never an envelope quoted inside a prompt.
func QuestionReplies(text string) []QuestionReply {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "# Context from my IDE setup:\n") {
		_, request, ok := strings.CutLast(text, "\n## My request for Codex:\n")
		if !ok {
			return nil
		}
		text = strings.TrimSpace(request)
	}
	body, ok := strings.CutPrefix(text, QuestionReplyStart)
	if !ok {
		return nil
	}
	body, ok = strings.CutSuffix(body, QuestionReplyEnd)
	if !ok {
		return nil
	}
	type wireReply struct {
		Answer   *string `json:"answer"`
		Question *string `json:"question"`
		ID       *string `json:"questionItemId"`
	}
	var wire []wireReply
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		var one wireReply
		if json.Unmarshal([]byte(body), &one) != nil {
			return nil
		}
		wire = []wireReply{one}
	} else if json.Unmarshal([]byte(body), &wire) != nil {
		return nil
	}
	var replies []QuestionReply
	for _, r := range wire {
		if r.Answer == nil || r.Question == nil || r.ID == nil {
			return nil
		}
		replies = append(replies, QuestionReply{*r.Answer, *r.Question, *r.ID})
	}
	return replies
}
func QuestionReplyIdentity(reply QuestionReply) (string, int, bool) {
	var id []jsontext.Value
	if json.Unmarshal([]byte(reply.QuestionItemID), &id) != nil || len(id) != 3 {
		return "", 0, false
	}
	var tool, item string
	var index int
	if json.Unmarshal(id[0], &tool) != nil || tool != "request_user_input_async" || json.Unmarshal(id[1], &item) != nil || json.Unmarshal(id[2], &index) != nil || index < 0 {
		return "", 0, false
	}
	return item, index, true
}
