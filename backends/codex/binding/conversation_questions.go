package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/contracts/observation"
)

type QuestionAnswer struct {
	ID      string `json:"id"`
	Choice  int    `json:"choice,omitzero"`
	Note    string `json:"note,omitzero"`
	Other   string `json:"other,omitzero"`
	Skipped bool   `json:"skipped,omitzero"`
}

func (a QuestionAnswer) valid() bool {
	if a.ID == "" || a.Choice < 0 {
		return false
	}
	if a.Choice > 0 {
		return a.Other == "" && !a.Skipped
	}
	return a.Note == "" && (a.Other != "" && !a.Skipped || a.Other == "" && a.Skipped)
}

func EncodeQuestionResponse(answers []QuestionAnswer) map[string]any {
	if answers == nil {
		answers = []QuestionAnswer{}
	}
	return map[string]any{"answers": answers}
}

type QuestionOption = observation.QuestionOption
type Question = observation.Question
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
func QuestionIdentity(thread, item string, request jsontext.Value) string {
	if len(request) > 0 {
		return thread + "\x00request/" + string(request)
	}
	return thread + "\x00item/" + item
}

const QuestionReplyStart = "<question_answers>"
const QuestionReplyEnd = "</question_answers>"

func EncodeQuestionReplies(replies []QuestionAnswer) string {
	data, _ := json.Marshal(replies)
	return QuestionReplyStart + string(data) + QuestionReplyEnd
}

// QuestionReplies accepts a complete reply, never an envelope quoted in a prompt.
func QuestionReplies(text string) []QuestionAnswer {
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
	var replies []QuestionAnswer
	if json.Unmarshal([]byte(body), &replies, json.RejectUnknownMembers(true)) != nil {
		return nil
	}
	seen := make(map[string]bool, len(replies))
	for _, reply := range replies {
		if !reply.valid() || seen[reply.ID] {
			return nil
		}
		seen[reply.ID] = true
	}
	return replies
}
