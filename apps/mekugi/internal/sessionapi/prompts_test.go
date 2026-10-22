package sessionapi

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
	"testing"
)

func TestPromptPublication(t *testing.T) {
	t.Parallel()
	s := NewLive()
	viewer := make(chan publication, subscriberBuffer)
	s.subscribers[viewer] = struct{}{}
	f := Frame{Input: &Input{}, Prompts: &Prompts{Questions: []QuestionCall{{ID: "call", Questions: []Question{{ID: "call/0", Text: "Which workspace?"}}}}}}
	if err := s.Publish(f); err != nil {
		t.Fatal(err)
	}
	read := func(kind string) Prompts {
		t.Helper()
		event := <-viewer
		if event.Kind != kind {
			t.Fatal(event)
		}
		var m struct {
			Prompts *Prompts `json:"prompts"`
		}
		if err := json.Unmarshal(event.Data, &m); err != nil || m.Prompts == nil {
			t.Fatalf("missing prompts: %+v (%v)", event, err)
		}
		return *m.Prompts
	}
	if p := read("snapshot"); len(p.Questions) != 1 || p.Questions[0].Questions[0].Text != "Which workspace?" {
		t.Fatalf("snapshot: %+v", p)
	}
	if err := s.Publish(f); err != nil {
		t.Fatal(err)
	}
	if len(viewer) != 0 {
		t.Fatal("unchanged prompts produced a patch")
	}
	f.Prompts.Questions = nil
	f.Prompts.Approvals = []Approval{{ID: "approval", Title: "Run command?", Choices: []Choice{{ID: 0, Label: "Allow"}, {ID: 1, Label: "Deny", Deny: true}}}}
	if err := s.Publish(f); err != nil {
		t.Fatal(err)
	}
	if p := read("patch"); len(p.Questions) != 0 || len(p.Approvals) != 1 || !p.Approvals[0].Choices[1].Deny {
		t.Fatalf("patch: %+v", p)
	}
	f.Prompts = &Prompts{}
	if err := s.Publish(f); err != nil {
		t.Fatal(err)
	}
	if p := read("patch"); len(p.Questions)+len(p.Approvals) != 0 {
		t.Fatalf("cleared: %+v", p)
	}
}

func TestPromptRequestAdmission(t *testing.T) {
	t.Parallel()
	s := NewLive()
	request := func(method, body string) testReply {
		t.Helper()
		result, failure := s.request(t.Context(), requestTarget{}, "session/"+method, []byte(body), make(chan struct{}, preparationLimit))
		return ownerReply(t, result, failure)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 2 {
			intent := <-s.Intents()
			if i == 0 {
				if intent.Action != AnswerQuestions || intent.Answer.Call != "q" || len(intent.Answer.Answers) != 1 || !intent.Answer.Answers[0].Skip {
					t.Error("answer identity lost")
				}
				intent.Reply <- ""
			} else {
				if intent.Action != DecideApproval || intent.Decide.Approval != "a" || intent.Decide.Choice != 2 || intent.Decide.Reason != "Keep it local" {
					t.Error("approval identity/reason lost")
				}
				intent.Reply <- "Already answered."
			}
		}
	}()
	if w := request("answer", `{"call":"q","answers":[{"question":"q/0","skip":true}]}`); w.Code != 202 {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	if w := request("decide", `{"approval":"a","choice":2,"reason":"Keep it local"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "Already answered.") {
		t.Fatalf("decide: %d %s", w.Code, w.Body)
	}
	<-done
	for _, path := range []string{"answer", "decide"} {
		if w := request(path, "{"); w.Code != 400 {
			t.Fatalf("malformed %s: %d", path, w.Code)
		}
		_, failure := New().request(t.Context(), requestTarget{}, "session/"+path, []byte("{}"), make(chan struct{}, preparationLimit))
		if failure == nil || failure.Data.Status != http.StatusMethodNotAllowed {
			t.Fatalf("replay %s: %v", path, failure)
		}
	}
	if w := request("decide", `{"approval":"a"}`); w.Code != 400 {
		t.Fatalf("missing choice: %d", w.Code)
	}
}
