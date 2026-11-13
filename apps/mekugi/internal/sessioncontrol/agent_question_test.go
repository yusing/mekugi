package sessioncontrol

import (
	"encoding/json/jsontext"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestAgentQuestionUsesRestrictedForkAndCleansUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "fork"}
		r := NewRequests[struct{}](transport)
		defer r.Close()
		done := make(chan questionResult, 1)
		go func() {
			answer, err := r.SideQuestion(t.Context(), &control.Member{Thread: "source", Session: "source-session", Settings: control.Settings{Effort: new("ultra")}}, "question")
			done <- questionResult{answer, err}
		}()
		r.DispatchQuestion(<-r.Questions())
		params := transport.sends[0].params.(map[string]any)
		if params["threadId"] != "source" || params["sandbox"] != "read-only" || params["approvalPolicy"] != "never" || params["config"] != nil {
			t.Fatalf("fork restrictions=%v", params)
		}
		deliver := func(id, next, body string) {
			t.Helper()
			response, _ := r.Take(id)
			transport.id = next
			if !r.ObserveAgentQuestion(appserver.Message{ID: jsontext.Value(id), Result: jsontext.Value(body)}, response) {
				t.Fatal("question response unhandled")
			}
		}
		deliver("fork", "start", `{"thread":{"id":"side"}}`)
		if transport.sends[1].method != "turn/start" || len(transport.sends[1].params.(map[string]any)["environments"].([]any)) != 0 {
			t.Fatal("side start lost environment restriction")
		}
		deliver("start", "archive", `{"turn":{"id":"side-turn"}}`)
		for _, event := range []appserver.Message{
			{Method: "item/completed", Params: jsontext.Value(`{"threadId":"side","turnId":"side-turn","item":{"id":"answer","type":"agentMessage","text":"answer"}}`)},
			{Method: "turn/completed", Params: jsontext.Value(`{"threadId":"side","turn":{"id":"side-turn","status":"completed"}}`)},
		} {
			if !r.ObserveAgentQuestion(event, Request[struct{}]{}) {
				t.Fatal("question event unhandled")
			}
		}
		// The answer does not wait for the fork's archive acknowledgement.
		if result := <-done; result.err != nil || result.answer != "answer" {
			t.Fatalf("result=%+v", result)
		}
		if transport.sends[len(transport.sends)-1].method != "thread/archive" {
			t.Fatal("completed question not archived")
		}
		deliver("archive", "unused", `{}`)
		if r.HasSides() || r.Pending() {
			t.Fatal("question cleanup retained correlation")
		}
	})
}

func TestAgentQuestionStartFailureReturnsAndAllowsCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "fork"}
		r := NewRequests[struct{}](transport)
		defer r.Close()
		done := make(chan error, 1)
		go func() {
			_, err := r.SideQuestion(t.Context(), &control.Member{Thread: "source", Session: "source"}, "question")
			done <- err
		}()
		r.DispatchQuestion(<-r.Questions())
		response, _ := r.Take("fork")
		failure := errors.New("native send failed")
		transport.err = failure
		r.ObserveAgentQuestion(appserver.Message{ID: jsontext.Value("fork"), Result: jsontext.Value(`{"thread":{"id":"side"}}`)}, response)
		if err := <-done; !errors.Is(err, failure) {
			t.Fatalf("start failure hidden: %v", err)
		}
		side := r.Side("side")
		if side.Starting || side.release.phase.Load() == forkClosing || !side.Closed || r.Pending() {
			t.Fatal("failed send retained an impossible acknowledgement")
		}
		transport.err = nil
		if err := r.ReleaseSide(side); err != nil || transport.sends[len(transport.sends)-1].method != "thread/archive" {
			t.Fatal("joined shutdown cannot release the failed question")
		}
	})
}

func TestAgentQuestionUnsolicitedArchiveFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "fork"}
		r := NewRequests[struct{}](transport)
		defer r.Close()
		done := make(chan questionResult, 1)
		go func() {
			answer, err := r.SideQuestion(t.Context(), &control.Member{Thread: "source", Session: "source"}, "question")
			done <- questionResult{answer, err}
		}()
		r.DispatchQuestion(<-r.Questions())
		response, _ := r.Take("fork")
		transport.id = "start"
		r.ObserveAgentQuestion(appserver.Message{ID: jsontext.Value("fork"), Result: jsontext.Value(`{"thread":{"id":"side"}}`)}, response)
		response, _ = r.Take("start")
		r.ObserveAgentQuestion(appserver.Message{ID: jsontext.Value("start"), Result: jsontext.Value(`{"turn":{"id":"turn"}}`)}, response)
		r.ObserveAgentQuestion(appserver.Message{Method: "thread/archived", Params: jsontext.Value(`{"threadId":"side"}`)}, Request[struct{}]{})
		result := <-done
		if result.err == nil || result.answer != "" || r.HasSides() {
			t.Fatalf("unexpected archive returned a successful answer or retained the fork: %+v", result)
		}
	})
}
