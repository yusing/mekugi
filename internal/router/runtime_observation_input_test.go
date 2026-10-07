package router

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestNativeObservationBashReorderedHookInput(t *testing.T) {
	for _, status := range []string{"completed", "failed", "stopped"} {
		t.Run(status, func(t *testing.T) {
			service, binding, client := observationHTTPFixture(t)
			token := service.Endpoint().Token
			if code, body := observationPost(t, service, client, token, observationRequest{Operation: "bind", Binding: binding}); code != http.StatusOK {
				t.Fatalf("bind: %d %s", code, body)
			}
			path := filepath.Join(binding.Workspace, "native.txt")
			nativeObservationWrite(t, path, "before\n")
			call := ObservationCall{Binding: binding, ID: "bash", Tool: "Bash", Command: "printf after > native.txt", Input: `{"command":"printf after > native.txt","description":"Write fixture","timeout":600000}`}
			if code, body := observationPost(t, service, client, token, observationRequest{Operation: "before", Call: call}); code != http.StatusOK {
				t.Fatalf("before: %d %s", code, body)
			}
			nativeObservationWrite(t, path, "after\n")
			post := call
			post.Input = `{"timeout":600000,"description":"Write fixture","command":"printf after > native.txt"}`
			request := observationRequest{Operation: "after", Call: post, Terminal: ObservationTerminal{Status: status}}
			if code, body := observationPost(t, service, client, token, request); code != http.StatusOK {
				t.Fatalf("after: %d %s", code, body)
			}
			history := nativeObservationHistory(t, service.owner.store, call, "after")
			if history.ChangeID == "" || len(history.ReviewFiles) != 1 || history.NativeObservation.Call.Input != post.Input {
				t.Fatalf("raw input or actual capture lost: %#v", history.NativeObservation)
			}
			request.Call = call
			if code, body := observationPost(t, service, client, token, request); code != http.StatusOK {
				t.Fatalf("retry: %d %s", code, body)
			}
			if service.owner.pendingCount.Load() != 0 {
				t.Fatal("completion left observation pending")
			}
		})
	}
}

func TestNativeObservationInputComparisonKeepsValues(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		equal               bool
	}{
		{"nested objects", `{"a":[{"b":2,"a":1}],"b":true}`, ` {"b": true,"a":[{"a":1,"b":2}]} `, true},
		{"escaped string", `{"a":"\u0061"}`, `{"a":"a"}`, true},
		{"changed command", `{"command":"original"}`, `{"command":"changed"}`, false},
		{"added option", `{"command":"original"}`, `{"command":"original","run_in_background":true}`, false},
		{"array order", `{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{"large integer", `{"n":9007199254740992}`, `{"n":9007199254740993}`, false},
		{"duplicate members", `{"a":1}`, `{"a":1,"a":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := ObservationCall{ID: "same", Tool: "Bash", Command: "original", Input: tc.before}
			b := a
			b.Input = tc.after
			if sameObservationCall(&a, &b) != tc.equal {
				t.Fatal("input equivalence lost or changed values admitted")
			}
			b.ID = "different"
			if sameObservationCall(&a, &b) {
				t.Fatal("different native identity admitted")
			}
			if a.Input != tc.before || b.Input != tc.after {
				t.Fatal("raw native inputs changed")
			}
		})
	}
}
