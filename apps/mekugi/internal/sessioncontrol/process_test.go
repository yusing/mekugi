package sessioncontrol

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

type processTransport struct {
	requestTransport
	fail string
}

func (s *processTransport) Send(method string, params any, request bool) (string, error) {
	s.id = fmt.Sprint(len(s.sends) + 1)
	s.err = nil
	if method == s.fail {
		s.err = errors.New("fixture transport failure")
	}
	return s.requestTransport.Send(method, params, request)
}

func TestStopProcessCancellationAndHostOutcome(t *testing.T) {
	for _, outcome := range []struct {
		name    string
		message appserver.Message
		failure string
	}{
		{"stopped", appserver.Message{Result: []byte(`{"terminated":true}`)}, ""},
		{"already ended", appserver.Message{Result: []byte(`{"terminated":false}`)}, "the process already ended or could not be stopped"},
		{"refused", appserver.Message{Error: &appserver.Error{Message: "fixture refusal"}}, "fixture refusal"},
		{"malformed", appserver.Message{Result: []byte(`{`)}, "invalid host termination response"},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			transport := new(processTransport)
			requests := NewRequests[string](transport)
			if err := requests.StopProcess("child", "42", "detached-view"); err != nil {
				t.Fatal(err)
			}
			if len(transport.sends) != 2 || transport.sends[0].method != "thread/inject_items" || transport.sends[1].method != "thread/backgroundTerminals/terminate" {
				t.Fatalf("cancellation dispatch: %+v", transport.sends)
			}
			assertProcessNotice(t, transport.sends[0], "child", processCancellationNotice("42"))
			request, ok := requests.Take("2")
			if !ok || request.Scope != "child" || request.Owner != "detached-view" {
				t.Fatalf("lost ownership: %+v", request)
			}
			handled, warning := requests.ProcessResponse(request, outcome.message)
			if !handled || (warning != "") != (outcome.failure != "") {
				t.Fatalf("handled=%t warning=%q", handled, warning)
			}
			if outcome.failure != "" {
				if !strings.Contains(warning, outcome.failure) || len(transport.sends) != 3 {
					t.Fatalf("missing failure: %q %+v", warning, transport.sends)
				}
				assertProcessNotice(t, transport.sends[2], "child", processTerminationFailureNotice("42", outcome.failure))
			} else if len(transport.sends) != 2 {
				t.Fatal("termination restarted work")
			}
			if _, ok := requests.Take("2"); ok {
				t.Fatal("response remained admitted")
			}
		})
	}
}

func TestStopProcessDeliveryFailures(t *testing.T) {
	for _, method := range []string{"thread/inject_items", "thread/backgroundTerminals/terminate"} {
		t.Run(method, func(t *testing.T) {
			transport := &processTransport{fail: method}
			requests := NewRequests[string](transport)
			if err := requests.StopProcess("main", "42", "view"); err == nil {
				t.Fatal("transport failure hidden")
			}
			if len(transport.sends) < 2 || transport.sends[1].method != "thread/backgroundTerminals/terminate" {
				t.Fatal("notice failure prevented stop")
			}
			if method == "thread/backgroundTerminals/terminate" {
				assertProcessNotice(t, transport.sends[2], "main", processTerminationFailureNotice("42", "fixture transport failure"))
			}
		})
	}
	transport := new(processTransport)
	requests := NewRequests[string](transport)
	if err := requests.StopProcess("main", "42", "view"); err != nil {
		t.Fatal(err)
	}
	request, _ := requests.Take("1")
	if handled, warning := requests.ProcessResponse(request, appserver.Message{Error: &appserver.Error{Message: "fixture refusal"}}); !handled || !strings.Contains(warning, "fixture refusal") {
		t.Fatalf("notice rejection hidden: %t %q", handled, warning)
	}
}

func assertProcessNotice(t *testing.T, send requestSend, thread, text string) {
	t.Helper()
	body, err := json.Marshal(send.params)
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		Thread string `json:"threadId"`
		Items  []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		t.Fatal(err)
	}
	if send.method != "thread/inject_items" || params.Thread != thread || len(params.Items) != 1 || params.Items[0].Role != "developer" || len(params.Items[0].Content) != 1 || params.Items[0].Content[0].Text != text {
		t.Fatalf("notice envelope: %s", body)
	}
}
