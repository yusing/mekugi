package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
)

func TestQuestionReplyCompactCodec(t *testing.T) {
	want := []QuestionAnswer{{ID: "q2", Choice: 2, Note: "because"}, {ID: "q1", Other: "written"}, {ID: "q3", Skipped: true}}
	text := EncodeQuestionReplies(want)
	if text != `<question_answers>[{"id":"q2","choice":2,"note":"because"},{"id":"q1","other":"written"},{"id":"q3","skipped":true}]</question_answers>` {
		t.Fatal(text)
	}
	if got := QuestionReplies(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: %+v", got)
	}
	for _, body := range []string{"", "null", "[]", "{}", `[{"id":"q1"}]`, `[{"id":"q1","choice":-1}]`, `[{"id":"q1","choice":1,"other":"x"}]`, `[{"id":"q1","skipped":true,"note":"x"}]`, `[{"id":"q1","other":""}]`, `[{"id":"q1","choice":1},{"id":"q1","other":"x"}]`, `[{"id":"q1","answer":"legacy"}]`} {
		if got := QuestionReplies(QuestionReplyStart + body + QuestionReplyEnd); len(got) != 0 {
			t.Fatalf("invalid %s decoded as %+v", body, got)
		}
	}
	if got := QuestionReplies(`<send_user_message_question_reply>[{"answer":"legacy","question":"Old?","questionItemId":"q1"}]</send_user_message_question_reply>`); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSharedRichQuestionDecoder(t *testing.T) {
	questions := `[{"id":"q1","question":"Free?"},{"id":"q2","header":null,"question":"Pick?","options":[{"label":"A","description":null},{"label":"B","description":"detail","preview":"a\nb","recommended":true}]}]`
	request, err := DecodeQuestionRequest(jsontext.Value(`{"questions":` + questions + `}`))
	if err != nil {
		t.Fatal(err)
	}
	var item ThreadItem
	if err := json.Unmarshal([]byte(`{"type":"agentMessage","questions":`+questions+`}`), &item); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.Questions, item.Questions) || len(request.Questions[0].Options) != 0 {
		t.Fatalf("decoders differ: %+v %+v", request, item)
	}
	o := item.Questions[1].Options[1]
	if o.Label != "B" || o.Description != "detail" || o.Preview != "a\nb" || !o.Recommended {
		t.Fatal(o)
	}
	if _, err := DecodeQuestionRequest(jsontext.Value(`{"questions":[{"id":"q1","question":"Invalid?","options":[3]}]}`)); err == nil {
		t.Fatal("numeric options accepted")
	}
}

func TestRetainedLegacyAsyncQuestions(t *testing.T) {
	var item ThreadItem
	if err := json.Unmarshal([]byte(`{"type":"agentMessage","delivery":"async","questions":[{"title":"Continue?","options":["Yes","No"]},{"id":"authored-id","title":"Why?"}]}`), &item); err != nil {
		t.Fatal(err)
	}
	if item.Questions[0].Question != "Continue?" || item.Questions[0].Options[0].Label != "Yes" || item.Questions[1].ID != "authored-id" {
		t.Fatal(item.Questions)
	}
	encoded, err := json.Marshal(item.Questions[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"title"`) || !strings.Contains(string(encoded), `"label":"Yes"`) {
		t.Fatal(string(encoded))
	}
}
