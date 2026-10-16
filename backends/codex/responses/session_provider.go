package responses

// Source: internal/router/provider_history.go:1:188@b3d178e3 provider-prefix evidence.

import (
	"crypto/sha256"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// providerHistory is transport evidence, never replay or filesystem authority.
// A cumulative digest avoids retaining a second quadratic copy of conversation
// content. Only a raw provider terminal can confirm a pending input fingerprint.
type providerHistory struct {
	digest    [sha256.Size]byte
	count     int
	confirmed bool
}

func (h providerHistory) append(input []jsontext.Value) (providerHistory, error) {
	for _, raw := range input {
		// Normalize JSON object order without rounding numbers or changing strings.
		value, err := responseevents.DecodeJSONValue(raw)
		if err != nil {
			return providerHistory{}, err
		}
		h.digest = sha256.Sum256(append(h.digest[:], responseevents.MustMarshalJSON(value)...))
		h.count++
	}
	return h, nil
}

// All ordinary projections run before this decision. Native item counts and
// feature-local invalidation hints cannot prove what a provider already holds.
// Reconcile selects only the projected prefix confirmed on this connection.
func (e *SessionExchange) Reconcile(request *Request, stateful bool) (string, error) {
	fields := request.Fields
	input, err := webSocketInput(fields["input"])
	if err != nil {
		return "", err
	}
	request.CachedInput = 0
	request.RebaseInput = false
	if !stateful {
		return "stateless_provider", nil
	}
	parentID := responseevents.StringField(fields, "previous_response_id")
	var cached providerHistory
	if parentID != "" && e.history.parent != nil && parentID == e.parentID {
		cached = e.history.parent.providerHistory
	}
	if cached.confirmed {
		cached, err = cached.append(e.steering)
		if err != nil {
			return "", err
		}
	}
	reason := "unconfirmed_provider"
	if cached.confirmed && cached.count <= len(input) {
		prefix, err := (providerHistory{}).append(input[:cached.count])
		if err != nil {
			return "", err
		}
		if prefix.digest == cached.digest {
			request.CachedInput = cached.count
			// Automatic successors have already started and cannot adopt unsent items.
			if !e.automatic || cached.count == len(input) {
				return "matching_prefix", nil
			}
		}
		reason = "changed_prefix"
	} else if cached.confirmed {
		reason = "shortened_prefix"
	}
	if e.automatic {
		return "", errors.New("automatic WebSocket successor changed or lacks confirmed cached provider history")
	}
	if parentID != "" {
		request.CachedInput = 0
		request.RebaseInput = true
		return reason, nil
	}
	return "full_request", nil
}

// Capture the submitted suffix and confirmed parent separately so steering
// acknowledged after preparation can be incorporated at actual admission.
func (e *SessionExchange) BeginProvider(fields map[string]jsontext.Value) error {
	input, err := webSocketInput(fields["input"])
	if err != nil {
		return err
	}
	e.providerOutput = nil
	e.history.providerHistory = providerHistory{}
	e.providerBase = providerHistory{}
	e.providerUsesParent = responseevents.StringField(fields, "previous_response_id") != ""
	if e.providerUsesParent && e.history.parent != nil {
		e.providerBase = e.history.parent.providerHistory
	}
	e.providerSuffix = input
	if e.automatic {
		e.providerSuffix = nil
	}
	return e.setProviderInput(e.steering)
}

func (e *SessionExchange) setProviderInput(steering []jsontext.Value) error {
	base := e.providerBase
	var err error
	if e.providerUsesParent {
		base, err = base.append(steering)
		if err != nil {
			return err
		}
	}
	e.providerInput, err = base.append(e.providerSuffix)
	// A full request needs no earlier provider evidence.
	e.providerInput.confirmed = !e.providerUsesParent || e.providerBase.confirmed
	return err
}

func (e *SessionExchange) ObserveProvider(body []byte) error {
	var event struct {
		Type     string         `json:"type"`
		Item     jsontext.Value `json:"item"`
		Response struct {
			ID     string           `json:"id"`
			Output []jsontext.Value `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &event, jsonv1.DefaultOptionsV1()); err != nil {
		return err
	}
	if event.Type == responseevents.Created {
		var steering []jsontext.Value
		for _, steer := range e.session.steers {
			if steer.id != "" && steer.parent == e.parentID {
				steering = append(steering, steer.input...)
			}
		}
		if err := e.setProviderInput(steering); err != nil {
			return err
		}
	}
	if event.Type == responseevents.OutputItemDone && len(event.Item) != 0 {
		if err := e.session.retain([]jsontext.Value{event.Item}); err != nil {
			return err
		}
		e.providerOutput = append(e.providerOutput, event.Item)
	}
	if !responseevents.Kind(event.Type).EndsExchange() {
		return nil
	}
	for _, item := range e.providerOutput {
		e.session.retainedBytes -= len(item)
	}
	output := mergeWebSocketOutput(e.providerOutput, event.Response.Output)
	if err := e.session.retain(output); err != nil {
		return err
	}
	for _, item := range output {
		e.session.retainedBytes -= len(item)
	}
	e.providerOutput = nil
	e.providerSuffix = nil
	terminal := responseevents.ObserveTerminal(body, true)
	if event.Response.ID == "" || !e.providerInput.confirmed ||
		(terminal != responseevents.TerminalCompleted && terminal != responseevents.TerminalSteered && terminal != responseevents.TerminalInterrupted) {
		return nil
	}
	state, err := e.providerInput.append(output)
	if err != nil {
		return err
	}
	e.history.providerHistory = state
	return nil
}
