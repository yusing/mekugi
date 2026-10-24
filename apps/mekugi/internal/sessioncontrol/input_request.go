package sessioncontrol

import (
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

// InputRequest is fully prepared native input with its admitted control identity.
type InputRequest struct {
	Thread, Turn, ID, Tier string
	Input                  []map[string]any
	// Settled receives the correlated host outcome without owning another ledger.
	// The callback must not block the host event loop.
	Settled func(turn string, err error)
}

// SendInput uses the same ledger and response correlation as every host request.
// Source: apps/mekugi/internal/router/app_server_input_queue.go:182:218@25e285b1.
func (r *Requests[T]) SendInput(input InputRequest, owner T) (string, error) {
	params := map[string]any{"threadId": input.Thread, "input": input.Input, "clientUserMessageId": input.ID}
	method := "turn/start"
	if input.Turn != "" {
		method = "turn/steer"
		params["expectedTurnId"] = input.Turn
	} else if input.Tier != "" {
		params["serviceTier"] = input.Tier
	}
	id, err := r.Send(method, params, "main", owner)
	if input.Settled != nil {
		if err != nil {
			input.Settled("", &binding.DispatchError{Outcome: binding.Uncertain, Method: method, Cause: err})
		} else {
			request := r.pending[id]
			request.input = &input
			r.pending[id] = request
		}
	}
	return id, err
}

// completeInput observes the ordinary human response while leaving its existing
// UI consumer responsible for composer settlement and restoration.
func (r *Requests[T]) completeInput(id string, request Request[T], message appserver.Message) {
	input := request.input
	if input == nil {
		return
	}
	request.input = nil
	r.pending[id] = request
	turn := input.Turn
	var err error
	if message.Error != nil {
		err = &binding.DispatchError{Outcome: binding.HostRejected, Method: request.Method, Cause: errors.New(message.Error.Message)}
	} else if turn == "" {
		var result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(message.Result, &result) != nil || result.Turn.ID == "" {
			err = &binding.DispatchError{Outcome: binding.Uncertain, Method: request.Method, Cause: errors.New("host accepted input without a confirmed turn identity")}
		} else {
			turn = result.Turn.ID
		}
	}
	input.Settled(turn, err)
}
