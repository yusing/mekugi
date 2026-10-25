package sessioncontrol

import (
	"encoding/json/jsontext"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeDeliveryWaitsForShellSettlement(t *testing.T) {
	for _, run := range []bool{false, true} {
		t.Run(map[bool]string{false: "message", true: "wake"}[run], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := &requestTransport{id: "native"}
				u := Client[struct{}]{Live: Live{Thread: "main"}, Requests: NewRequests[struct{}](transport)}
				u.Shell.Begin("")
				u.Shell.WakeAfterSettlement()
				input := control.NativeInput{Thread: "main", ID: "message", Kind: "MESSAGE", Message: "retained payload", Run: run}
				if run {
					input.Wake = &control.WaitResult{ID: "wait", State: "completed"}
				}
				result := make(chan error, 1)
				go func() { _, err := u.Requests.Input(t.Context(), input); result <- err }()
				synctest.Wait()
				for _, settle := range []func(){func() {}, func() { u.Started("shell") }, func() { u.Completed(true) }} {
					settle()
					if u.Calls() != nil || len(transport.sends) != 0 {
						t.Fatal("native delivery overlapped actual shell execution or its acknowledgement")
					}
				}
				u.Shell.Acknowledge(false, u.Turn)
				u.Requests.Dispatch(<-u.Calls())
				want := "thread/inject_items"
				if run {
					want = "turn/start"
				}
				if len(transport.sends) != 1 || transport.sends[0].method != want {
					t.Fatalf("settled shell did not release delivery: %+v", transport.sends)
				}
				u.Requests.CompleteCall(appserver.Message{ID: jsontext.Value(`native`), Result: jsontext.Value(`{"turn":{"id":"continued"}}`)})
				if err := <-result; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestNativeFollowupPreservesActiveTurnAdmission(t *testing.T) {
	for _, completed := range []bool{false, true} {
		for _, rejected := range []bool{false, true} {
			t.Run(fmt.Sprintf("completed=%t/rejected=%t", completed, rejected), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					transport := &requestTransport{id: "followup"}
					u := Client[struct{}]{Live: Live{Thread: "child", Turn: "active"}, Requests: NewRequests[struct{}](transport)}
					result := make(chan error, 1)
					go func() {
						_, err := u.Requests.Input(t.Context(), control.NativeInput{Thread: "child", ID: "followup", Run: true, Kind: "NEW_TASK", Message: "steer current work"})
						result <- err
					}()
					u.Dispatch(<-u.Calls())
					if completed {
						u.Completed(true)
					}
					message := appserver.Message{ID: jsontext.Value(`followup`), Result: jsontext.Value(`{"turn":{"id":"active","status":"inProgress"}}`)}
					if rejected {
						message.Error = &appserver.Error{Message: "rejected"}
					}
					if !u.CompleteCall(message) {
						t.Fatal("missing follow-up acknowledgement")
					}
					if err := <-result; (err != nil) != rejected {
						t.Fatalf("follow-up outcome: %v", err)
					}
					// Steering has no second turn/started notification.
					if u.Starting() || !u.AcceptsShell() || !u.AcceptsInput() || !u.AcceptsSettings() {
						t.Fatalf("follow-up wedged admission: %+v", u.Live)
					}
				})
			})
		}
	}
}

func TestNativeStartRejectionReleasesShellGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "native"}
		u := Client[struct{}]{Live: Live{Thread: "main"}, Requests: NewRequests[struct{}](transport)}
		result := make(chan error, 1)
		go func() {
			_, err := u.Requests.Input(t.Context(), control.NativeInput{Thread: "main", ID: "wake", Run: true})
			result <- err
		}()
		call := <-u.Calls()
		u.Dispatch(call)
		if u.AcceptsShell() || !u.Starting() {
			t.Fatal("dispatched start left shell admission open")
		}
		if !u.CompleteCall(appserver.Message{ID: jsontext.Value(`native`), Error: &appserver.Error{Message: "rejected"}}) {
			t.Fatal("missing start rejection")
		}
		if err := <-result; err == nil || !u.AcceptsShell() {
			t.Fatalf("rejection retained the start gate: %v", err)
		}
		u.Dispatch(call)
		if !u.AcceptsShell() || len(transport.sends) != 1 {
			t.Fatal("duplicate dispatch revived the settled start")
		}
	})
}
