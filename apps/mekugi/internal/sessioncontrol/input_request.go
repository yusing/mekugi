package sessioncontrol

// InputRequest is fully prepared native input with its admitted control identity.
type InputRequest struct {
	Thread, Turn, ID, Tier string
	Input                  []map[string]any
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
	return r.Send(method, params, "main", owner)
}
