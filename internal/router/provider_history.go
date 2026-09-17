package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

// providerHistory is transport evidence, never replay or filesystem authority.
// A cumulative digest avoids retaining a second quadratic copy of conversation
// content. Only a raw provider terminal can confirm a pending input fingerprint.
type providerHistory struct {
	digest    [sha256.Size]byte
	count     int
	confirmed bool
}

func (h providerHistory) append(input []json.RawMessage) (providerHistory, error) {
	for _, raw := range input {
		// Normalize JSON object order without rounding numbers or changing strings.
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return providerHistory{}, err
		}
		h.digest = sha256.Sum256(append(h.digest[:], mustMarshalJSON(value)...))
		h.count++
	}
	return h, nil
}

// All ordinary projections run before this decision. Native item counts and
// feature-local invalidation hints cannot prove what a provider already holds.
func (e *webSocketExchange) reconcileProviderHistory(request *parsedResponsesRequest, body []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	input, err := webSocketInput(fields["input"])
	if err != nil {
		return err
	}
	request.cachedInput = 0
	request.rebaseInput = false
	e.reconciliationReason = "full_request"
	if isChatCompletionsModel(request.model()) {
		e.reconciliationReason = "stateless_provider"
		return nil
	}
	parentID := jsonString(fields, "previous_response_id")
	var cached providerHistory
	if parentID != "" && e.history.parent != nil && parentID == e.parentID {
		cached = e.history.parent.providerHistory
	}
	if cached.confirmed {
		cached, err = cached.append(e.steering)
		if err != nil {
			return err
		}
	}
	reason := "unconfirmed_provider"
	if cached.confirmed && cached.count <= len(input) {
		prefix, err := (providerHistory{}).append(input[:cached.count])
		if err != nil {
			return err
		}
		if prefix.digest == cached.digest {
			request.cachedInput = cached.count
			e.reconciliationReason = "matching_prefix"
			// An automatic successor has already started; none of this request
			// will be sent. It cannot adopt additional projected items either.
			if !e.automatic || cached.count == len(input) {
				return nil
			}
		}
		reason = "changed_prefix"
	} else if cached.confirmed {
		reason = "shortened_prefix"
	}
	if e.automatic {
		return errors.New("automatic WebSocket successor changed or lacks confirmed cached provider history")
	}
	if parentID != "" {
		request.cachedInput = 0
		request.rebaseInput = true
		e.reconciliationReason = reason
	}
	return nil
}

// Capture the submitted suffix and confirmed parent separately so steering
// acknowledged after preparation can be incorporated at actual admission.
func (e *webSocketExchange) beginProviderHistory(fields map[string]json.RawMessage) error {
	input, err := webSocketInput(fields["input"])
	if err != nil {
		return err
	}
	e.providerOutput = nil
	e.history.providerHistory = providerHistory{}
	e.providerBase = providerHistory{}
	e.providerUsesParent = jsonString(fields, "previous_response_id") != ""
	if e.providerUsesParent && e.history.parent != nil {
		e.providerBase = e.history.parent.providerHistory
	}
	e.providerSuffix = input
	if e.automatic {
		e.providerSuffix = nil
	}
	return e.setProviderInput(e.steering)
}

func (e *webSocketExchange) setProviderInput(steering []json.RawMessage) error {
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

func (e *webSocketExchange) observeProviderHistory(body []byte) error {
	var event struct {
		Type     string          `json:"type"`
		Item     json.RawMessage `json:"item"`
		Response struct {
			ID     string            `json:"id"`
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return err
	}
	if event.Type == responseevents.Created {
		var steering []json.RawMessage
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
		if err := e.session.retain([]json.RawMessage{event.Item}); err != nil {
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
		(terminal != responseevents.TerminalCompleted && terminal != responseevents.TerminalSteered) {
		return nil
	}
	state, err := e.providerInput.append(output)
	if err != nil {
		return err
	}
	e.history.providerHistory = state
	return nil
}
