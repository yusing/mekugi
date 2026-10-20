package responses

// Source: internal/router/server_websocket.go:211:910@b3d178e3 connection-local history.

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"maps"
	"slices"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// MaxRequestBytes bounds incoming and reconstructed Responses requests.
const MaxRequestBytes = 32 << 20

// InvalidRequestError distinguishes client-invalid messages from provider failures.
type InvalidRequestError struct{ Message string }

func (e *InvalidRequestError) Error() string { return e.Message }

// SessionHistory owns one connection's synchronous protocol state. The zero value
// is ready for use. It retains no sockets, execution authority or durable records.
type SessionHistory struct {
	histories     map[string]*historyNode
	lastID        string
	steers        []pendingSteer
	retainedBytes int
}

// SessionExchange owns the state of one prepared response on its connection.
type SessionExchange struct {
	session            *SessionHistory
	history            *historyNode
	automatic          bool
	parentID           string
	steering           []jsontext.Value
	providerInput      providerHistory
	providerBase       providerHistory
	providerSuffix     []jsontext.Value
	providerUsesParent bool
	providerOutput     []jsontext.Value
}

func (e *SessionExchange) Automatic() bool { return e.automatic }

// RestoreProviderModel keeps automatic successors on their already-sent contract.
func (e *SessionExchange) RestoreProviderModel(request *Request) error {
	if !e.automatic {
		return nil
	}
	parent := e.history.parent
	if parent == nil || parent.providerModel == "" {
		return errors.New("automatic WebSocket successor has no retained provider model")
	}
	request.Fields["model"] = responseevents.MustMarshalJSON(parent.providerModel)
	if len(parent.providerReasoning) == 0 {
		delete(request.Fields, "reasoning")
	} else {
		request.Fields["reasoning"] = parent.providerReasoning
	}
	return nil
}

func (e *SessionExchange) RememberProviderModel(request Request) {
	e.history.providerModel = request.Model()
	e.history.providerReasoning = request.Fields["reasoning"]
}

// AddSteering retains a submitted steer before the caller writes it upstream.
func (s *SessionHistory) AddSteering(fields map[string]jsontext.Value) error {
	input, err := webSocketInput(fields["input"])
	if err != nil {
		return err
	}
	if err := s.retain(input); err != nil {
		return err
	}
	s.steers = append(s.steers, pendingSteer{parent: responseevents.StringField(fields, "previous_response_id"), input: input})
	return nil
}

// Prepare reconstructs an explicit command or an already-admitted automatic response.
func (s *SessionHistory) Prepare(command, firstEvent []byte) (*SessionExchange, Request, error) {
	automatic := len(command) == 0
	var fields map[string]jsontext.Value
	if !automatic {
		if err := json.Unmarshal(command, &fields, jsonv1.DefaultOptionsV1()); err != nil || fields == nil {
			return nil, Request{}, &InvalidRequestError{Message: "invalid response.create"}
		}
		if value := fields["stream_id"]; len(value) != 0 && string(value) != "null" {
			return nil, Request{}, &InvalidRequestError{Message: "multiplexed Responses WebSocket lanes are not supported"}
		}
	} else {
		var event struct {
			Response struct {
				Parent string `json:"previous_response_id"`
			} `json:"response"`
		}
		_ = json.Unmarshal(firstEvent, &event, jsonv1.DefaultOptionsV1())
		parent := event.Response.Parent
		if parent == "" {
			parent = s.lastID
		}
		fields = map[string]jsontext.Value{"previous_response_id": responseevents.MustMarshalJSON(parent)}
	}
	parentID := responseevents.StringField(fields, "previous_response_id")
	var parent *historyNode
	if parentID != "" {
		parent = s.histories[parentID]
		if parent == nil {
			if automatic {
				return nil, Request{}, errors.New("provider automatic response names an unretained previous_response_id")
			}
			return nil, Request{}, &InvalidRequestError{Message: "previous_response_id is not retained on this Responses WebSocket"}
		}
	}
	if !automatic {
		s.retainedBytes += len(command)
		if s.retainedBytes > MaxResponseBytes {
			return nil, Request{}, errors.New("responses WebSocket history exceeds the router buffer budget")
		}
	}
	input, err := webSocketInput(fields["input"])
	if err != nil {
		return nil, Request{}, err
	}
	if err := s.retain(input); err != nil {
		return nil, Request{}, err
	}
	var steering []jsontext.Value
	for _, steer := range s.steers {
		if steer.id != "" && steer.parent == parentID {
			steering = append(steering, steer.input...)
		}
	}
	settings := make(map[string]jsontext.Value)
	if automatic && parent != nil {
		maps.Copy(settings, parent.settings)
	}
	maps.Copy(settings, fields)
	delete(settings, "type")
	delete(settings, "input")
	delete(settings, "previous_response_id")
	// generate=false is a prewarm operation, never an inherited setting.
	delete(settings, "generate")
	requestFields := maps.Clone(settings)
	if generate, ok := fields["generate"]; ok {
		requestFields["generate"] = generate
	}
	var fullInput []jsontext.Value
	if parent != nil {
		fullInput = parent.items()
	}
	fullInput = append(fullInput, steering...)
	fullInput = append(fullInput, input...)
	requestFields["input"] = responseevents.MustMarshalJSON(fullInput)
	requestFields["stream"] = jsontext.Value("true")
	if parentID != "" {
		requestFields["previous_response_id"] = responseevents.MustMarshalJSON(parentID)
	}
	body, err := responseevents.MarshalJSON(requestFields)
	if err != nil {
		return nil, Request{}, err
	}
	if len(body) > MaxRequestBytes {
		return nil, Request{}, errors.New("responses WebSocket reconstructed request exceeds the router buffer budget")
	}
	parsed, err := ParseRequest(body)
	if err != nil {
		if automatic {
			return nil, Request{}, err
		}
		return nil, Request{}, &InvalidRequestError{Message: err.Error()}
	}
	history := &historyNode{parent: parent, input: input, settings: settings}
	return &SessionExchange{session: s, automatic: automatic, history: history, parentID: parentID, steering: steering}, parsed, nil
}

// Histories share their immutable ancestors rather than retaining quadratic
// copies of a growing conversation. Native client-visible items are kept for
// preparation; a separate bounded fingerprint records confirmed provider history.
type historyNode struct {
	parent   *historyNode
	input    []jsontext.Value
	output   []jsontext.Value
	settings map[string]jsontext.Value
	// Automatic steering successors execute the already-sent model contract.
	providerModel     string
	providerReasoning jsontext.Value
	providerHistory   providerHistory
}

func (h *historyNode) items() []jsontext.Value {
	var ancestry []*historyNode
	for node := h; node != nil; node = node.parent {
		ancestry = append(ancestry, node)
	}
	var items []jsontext.Value
	for _, a := range slices.Backward(ancestry) {
		items = append(items, a.input...)
		items = append(items, a.output...)
	}
	return items
}

type pendingSteer struct {
	id, parent string
	input      []jsontext.Value
}

func (s *SessionHistory) retain(items []jsontext.Value) error {
	for _, item := range items {
		s.retainedBytes += len(item)
	}
	if s.retainedBytes > MaxResponseBytes {
		return errors.New("responses WebSocket history exceeds the router buffer budget")
	}
	return nil
}

func webSocketInput(raw jsontext.Value) ([]jsontext.Value, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text, jsonv1.DefaultOptionsV1()) == nil {
		return []jsontext.Value{responseevents.MustMarshalJSON(map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]string{"type": "input_text", "text": text}},
		})}, nil
	}
	var items []jsontext.Value
	if err := json.Unmarshal(raw, &items, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, &InvalidRequestError{Message: "decode WebSocket input: " + err.Error()}
	}
	return items, nil
}

func (s *SessionHistory) ObserveControl(body []byte) {
	var event struct {
		Type  string `json:"type"`
		Steer struct {
			ID     string `json:"id"`
			Parent string `json:"previous_response_id"`
		} `json:"steer"`
	}
	if json.Unmarshal(body, &event, jsonv1.DefaultOptionsV1()) != nil {
		return
	}
	for index := range s.steers {
		steer := &s.steers[index]
		switch event.Type {
		case responseevents.SteerAccepted:
			if steer.id == "" && steer.parent == event.Steer.Parent {
				steer.id = event.Steer.ID
				return
			}
		case responseevents.SteerFailed:
			if event.Steer.ID != "" && steer.id == event.Steer.ID ||
				event.Steer.ID == "" && steer.id == "" && steer.parent == event.Steer.Parent {
				s.steers = append(s.steers[:index], s.steers[index+1:]...)
				return
			}
		}
	}
}

// Acknowledgement/failure can arrive after a continuation was sent. Admission,
// not preparation's snapshot, owns native history. Legal steering contains user
// messages, never developer instruction or tool carriers.
func (s *SessionHistory) commitSteering(history *historyNode, parent string) {
	var committed []jsontext.Value
	remaining := s.steers[:0]
	for _, steer := range s.steers {
		if steer.id == "" || steer.parent != parent {
			remaining = append(remaining, steer)
		} else {
			committed = append(committed, steer.input...)
		}
	}
	s.steers = remaining
	history.input = append(committed, history.input...)
}

func (e *SessionExchange) ObserveOutput(payload []byte) error {
	s := e.session
	var event struct {
		Type     string `json:"type"`
		Response struct {
			ID     string           `json:"id"`
			Output []jsontext.Value `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &event, jsonv1.DefaultOptionsV1()); err != nil {
		return err
	}
	if event.Type == responseevents.OutputItemDone {
		var item struct {
			Item jsontext.Value `json:"item"`
		}
		if json.Unmarshal(payload, &item, jsonv1.DefaultOptionsV1()) == nil && len(item.Item) != 0 {
			if err := s.retain([]jsontext.Value{item.Item}); err != nil {
				return err
			}
			e.history.output = append(e.history.output, item.Item)
		}
	}
	if event.Type == responseevents.Created {
		// Creation commits queued steering. Never replay accepted input after
		// this point, even if delivery or the successor subsequently fails.
		s.commitSteering(e.history, e.parentID)
	}
	if responseevents.Kind(event.Type).Terminal() {
		if event.Response.ID == "" {
			return errors.New("provider terminal response has no id")
		}
		if len(event.Response.Output) != 0 {
			// Terminal snapshots can omit completed streamed calls, especially
			// when an empty provider output gains router commentary. Reconcile
			// matching items without discarding the host's completed history.
			output := mergeWebSocketOutput(e.history.output, event.Response.Output)
			retained := s.retainedBytes
			for _, item := range e.history.output {
				s.retainedBytes -= len(item)
			}
			if err := s.retain(output); err != nil {
				s.retainedBytes = retained
				return err
			}
			e.history.output = output
		}
		if s.histories == nil {
			s.histories = make(map[string]*historyNode)
		}
		s.histories[event.Response.ID] = e.history
		s.lastID = event.Response.ID
	}
	return nil
}

// Keep delivered order while accepting final metadata for matching identities.
// Items without an ID can only be deduplicated by their exact retained encoding.
func mergeWebSocketOutput(streamed, terminal []jsontext.Value) []jsontext.Value {
	output := slices.Clone(streamed)
	positions := make(map[string]int, len(streamed)+len(terminal))
	key := func(raw jsontext.Value) string {
		var item struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &item, jsonv1.DefaultOptionsV1()) == nil && item.ID != "" {
			return "id:" + item.ID
		}
		return "raw:" + string(raw)
	}
	for index, item := range streamed {
		positions[key(item)] = index
	}
	for _, item := range terminal {
		identity := key(item)
		if index, exists := positions[identity]; exists {
			output[index] = item
		} else {
			positions[identity] = len(output)
			output = append(output, item)
		}
	}
	return output
}
