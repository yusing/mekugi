package sessioncontrol

import (
	json "encoding/json/v2"
	"errors"
	"os/exec"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

// Client binds one actual host connection to its application control lifetime.
// Main replacement leaves the connection, startup policy, title saves and
// independent request scopes alive. The event-loop owner serializes access.
type Client[T any] struct {
	Host *appserver.Client
	Live
	Startup  Startup
	Names    Names
	Requests *Requests[T]
	BTW      *Side
	retired  map[string]string // Retired thread to its former session root.
}

// Calls admits feature requests after auxiliary execution and acknowledgement
// settle. The existing channel and ledger retain queued delivery ownership.
func (u *Client[T]) Calls() <-chan *HostCall {
	if u.Shell.BlocksInput(u.Turn) || u.Compaction.Pending() {
		return nil
	}
	return u.Requests.Calls()
}

// Dispatch keeps native model starts in the same pre-notification input gate as
// composer starts. The request ledger still owns issuance and acknowledgement.
func (u *Client[T]) Dispatch(call *HostCall) {
	if call.phase.Load() != callQueued {
		return
	}
	if call.method == "thread/compact/start" {
		if call.scope != u.Thread || u.Turn != "" || u.Compaction.Pending() {
			call.phase.Store(callCanceled)
			call.result <- hostCallResult{err: &binding.DispatchError{Outcome: binding.NotDispatched, Method: call.method, Cause: errors.New("compaction requires settled host work")}}
			return
		}
	}
	u.Requests.Dispatch(call)
	if call.sent && call.method == "thread/compact/start" {
		u.Compaction.Begin(CompactIntent{})
		u.Compaction.call = call
	}
	if call.phase.Load() == callDispatched && call.method == "turn/start" && call.scope == u.Thread {
		u.AwaitingTurn = true
	}
}

func (u *Client[T]) CompleteCall(message appserver.Message) bool {
	request, _ := u.Requests.Lookup(string(message.ID))
	if request.call != nil && request.Method == "thread/compact/start" && message.Method == "" {
		u.Requests.Take(string(message.ID))
		u.CompactResponse(message.Error != nil)
		return true
	}
	if !u.Requests.CompleteCall(message) {
		return false
	}
	if request.Method == "turn/start" && request.Scope == u.Thread {
		var result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(message.Result, &result)
		// A follow-up can steer an existing turn without another start event.
		if message.Error != nil || result.Turn.ID != "" && result.Turn.ID == u.Turn {
			u.AwaitingTurn = false
		}
	}
	return true
}

// Start constructs the sole request ledger over the backend's actual connection.
// The observer runs before queued delivery under the backend's bounded-observer contract.
func Start[T any](cmd *exec.Cmd, observe func(appserver.Message)) (Client[T], error) {
	host, err := appserver.Start(cmd, observe)
	if err != nil {
		return Client[T]{}, err
	}
	return Client[T]{Host: host, Requests: NewRequests[T](host)}, nil
}

// Source: apps/mekugi/internal/router/app_server_ui.go:471:478@[d4602352] ordinary Main request routing.
// Request uses the Main correlation scope, except for connection initialization.
func (u *Client[T]) Request(method string, params any) error {
	var owner T
	if method == "initialize" {
		_, err := u.Requests.Initialize(owner)
		return err
	}
	_, err := u.Requests.Send(method, params, "main", owner)
	return err
}

// RetireThreads rejects late observations, without canceling any host operation.
func (u *Client[T]) RetireThreads(root string, children ...string) {
	if u.retired == nil {
		u.retired = make(map[string]string)
	}
	u.retired[root] = root
	for _, thread := range children {
		u.retired[thread] = root
	}
}

// Source: apps/mekugi/internal/router/app_server_commands.go:195:221@[d4602352] retired-thread admission.
// RetiredEvent admits replies independently and follows retired parent identity.
func (u *Client[T]) RetiredEvent(m appserver.Message) bool {
	if len(u.retired) == 0 || m.Method == "" {
		return false
	}
	var p struct {
		ThreadID string             `json:"threadId"`
		Thread   binding.ThreadInfo `json:"thread"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return false
	}
	return u.RetiredThread(p.Thread.ID, p.Thread.ParentThreadID) || u.RetiredThread(p.ThreadID, "")
}

// RetiredThread also admits authoritative ancestry returned by metadata reads.
func (u *Client[T]) RetiredThread(thread, parent string) bool {
	if thread == "" {
		return false
	}
	if root := u.retired[parent]; root != "" {
		u.retired[thread] = root
	}
	return u.retired[thread] != ""
}

// Readmit accepts a returning session and its descendants.
func (u *Client[T]) Readmit(root string) {
	delete(u.retired, root)
	for thread, owner := range u.retired {
		if owner == root {
			delete(u.retired, thread)
		}
	}
}
