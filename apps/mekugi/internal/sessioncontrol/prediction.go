package sessioncontrol

import (
	"crypto/rand"
	json "encoding/json/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

// PredictionForkSupported follows the desktop's experimentalPredictionForkMode
// capability floor. Older hosts can ignore an unknown fork field, so request
// failure alone cannot safely discover this capability.
// Source: ChatGPT desktop 26.1013.40934, src-Dtwb7yKT.js zi.
func PredictionForkSupported(version string) bool {
	version, _, _ = strings.Cut(version, "+")
	core, prerelease, _ := strings.Cut(version, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	var numbers [3]int
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return false
		}
		numbers[i] = value
	}
	if numbers[0] != 0 {
		return numbers[0] > 0
	}
	if numbers[1] != 162 {
		return numbers[1] > 162
	}
	if numbers[2] != 0 {
		return numbers[2] > 0
	}
	if prerelease == "" {
		return true
	}
	name, number, ok := strings.Cut(prerelease, ".")
	if !ok {
		return name == "beta" || name == "rc"
	}
	if name == "beta" || name == "rc" {
		return true
	}
	if name != "alpha" {
		return false
	}
	number, _, _ = strings.Cut(number, ".")
	alpha, err := strconv.Atoi(number)
	return err == nil && alpha >= 20
}

// Prediction retains host correlation after its composer destination is retired.
type Prediction struct {
	SourceThread, SourceTurn, Trigger string
	Prompt, Effort                    string
	UnsupportedModels                 []string
	Thread, Turn, Text                string
	Closed, Completed                 bool
	pending                           bool
	release                           forkRelease
}

func (r *Requests[T]) sendPrediction(p *Prediction, method string, params any) error {
	var owner T
	id, err := r.Send(method, params, "", owner)
	if err == nil {
		request := r.pending[id]
		request.Prediction = p
		r.pending[id] = request
	} else {
		p.pending = false
		p.release.Failed()
	}
	return err
}

func (r *Requests[T]) StartPrediction(p *Prediction) error {
	p.pending = true
	return r.sendPrediction(p, "thread/resume", map[string]any{"threadId": p.SourceThread})
}

// PredictionPending bounds auxiliary work when a cancelled fork is still closing.
func (r *Requests[T]) PredictionPending() bool {
	r.sidesMu.RLock()
	active := len(r.predictions) != 0
	r.sidesMu.RUnlock()
	if active {
		return true
	}
	for _, request := range r.pending {
		if request.Prediction != nil {
			return true
		}
	}
	return false
}

func (r *Requests[T]) ClosePrediction(p *Prediction) error {
	if p == nil {
		return nil
	}
	p.Closed = true
	return r.releasePrediction(p)
}

func (r *Requests[T]) releasePrediction(p *Prediction) error {
	if p.pending || p.Thread == "" {
		return nil
	}
	turn := p.Turn
	if p.Completed {
		turn = ""
	}
	method := p.release.Next(turn, "thread/unsubscribe")
	if method == "" {
		return nil
	}
	params := map[string]any{"threadId": p.Thread}
	if method == "turn/interrupt" {
		params["turnId"] = p.Turn
	}
	return r.sendPrediction(p, method, params)
}

// ObservePrediction runs before Main/side admission and uses their one ledger.
// Source: ChatGPT desktop 26.1013.40934, src-DsLVGakC.js Jq/An.
func (c *Client[T]) ObservePrediction(m appserver.Message, response Request[T]) (bool, error) {
	r := c.Requests
	p := response.Prediction
	if m.Method == "" {
		if p == nil {
			return false, nil
		}
		if response.Method == "thread/unsubscribe" {
			if m.Error != nil {
				p.release.Failed()
				return true, nil
			}
			p.release.Complete()
			r.sidesMu.Lock()
			delete(r.predictions, p.Thread)
			r.sidesMu.Unlock()
			c.RetireThreads(p.Thread)
			return true, nil
		}
		if response.Method == "turn/interrupt" {
			p.release.InterruptAcknowledged()
			p.Completed = true
			return true, r.ClosePrediction(p)
		}
		p.pending = false
		if m.Error != nil {
			return true, r.ClosePrediction(p)
		}
		var result struct {
			Thread            binding.ThreadInfo `json:"thread"`
			Turn              binding.Turn       `json:"turn"`
			Model             string             `json:"model"`
			ModelProvider     string             `json:"modelProvider"`
			CollaborationMode struct {
				Mode string `json:"mode"`
			} `json:"collaborationMode"`
		}
		if json.Unmarshal(m.Result, &result) != nil {
			return true, r.ClosePrediction(p)
		}
		switch response.Method {
		case "thread/resume":
			if p.Closed || result.Thread.ID != p.SourceThread || result.ModelProvider != "openai" || result.CollaborationMode.Mode == "plan" || inferencegateway.IsThirdPartyModel(result.Model) || slices.Contains(p.UnsupportedModels, result.Model) {
				return true, r.ClosePrediction(p)
			}
			if turns := result.Thread.Turns; len(turns) == 0 || turns[len(turns)-1].ID != p.SourceTurn || turns[len(turns)-1].Status != "completed" {
				return true, r.ClosePrediction(p)
			}
			p.pending = true
			return true, r.sendPrediction(p, "thread/fork", map[string]any{"threadId": p.SourceThread, "experimentalPredictionMode": true, "ephemeral": true, "excludeTurns": true, "threadSource": "composer_predictions"})
		case "thread/fork":
			if result.Thread.ID == "" || result.Thread.ID == p.SourceThread {
				return true, r.ClosePrediction(p)
			}
			p.Thread = result.Thread.ID
			r.sidesMu.Lock()
			if r.predictions == nil {
				r.predictions = make(map[string]*Prediction)
			}
			r.predictions[p.Thread] = p
			r.sidesMu.Unlock()
			if p.Closed {
				return true, r.ClosePrediction(p)
			}
			p.pending = true
			params := map[string]any{"threadId": p.Thread, "turnTrigger": "composer_predictions", "clientUserMessageId": rand.Text(), "input": appserver.Input(p.Prompt), "responsesapiClientMetadata": map[string]string{"prediction_source_thread_id": p.SourceThread, "prediction_source_turn_id": p.SourceTurn, "prediction_trigger": p.Trigger}}
			if p.Effort != "" {
				params["effort"] = p.Effort
			}
			return true, r.sendPrediction(p, "turn/start", params)
		case "turn/start":
			if p.Completed {
				return true, r.releasePrediction(p)
			}
			if result.Turn.ID == "" {
				return true, r.ClosePrediction(p)
			}
			p.Turn = result.Turn.ID
			if p.Closed {
				return true, r.ClosePrediction(p)
			}
		}
		return true, nil
	}
	r.sidesMu.RLock()
	active := len(r.predictions) != 0
	r.sidesMu.RUnlock()
	if !active && m.Method != "thread/started" {
		return false, nil
	}
	var event binding.Notification
	// Notification uses explicit camel-case fields, including early thread/started.
	var source struct {
		Thread struct {
			ID     string `json:"id"`
			Source string `json:"threadSource"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(m.Params, &source)
	if m.Method == "thread/started" && source.Thread.Source == "composer_predictions" {
		return true, nil
	}
	if json.Unmarshal(m.Params, &event) != nil {
		return false, nil
	}
	r.sidesMu.RLock()
	p = r.predictions[event.ThreadID]
	r.sidesMu.RUnlock()
	if p == nil {
		return false, nil
	}
	if p.Completed && m.Method != "thread/closed" {
		return true, nil
	}
	if len(m.ID) != 0 {
		return true, r.ClosePrediction(p)
	}
	if event.TurnID != "" && p.Turn != "" && event.TurnID != p.Turn {
		return true, nil
	}
	switch m.Method {
	case "turn/started":
		p.Turn = event.Turn.ID
		if p.Closed {
			return true, r.ClosePrediction(p)
		}
	case "item/agentMessage/delta":
		if len(p.Text)+len(event.Delta) <= 64<<10 {
			p.Text += event.Delta
		} else {
			return true, r.ClosePrediction(p)
		}
	case "item/completed":
		if event.Item.Type == "agentMessage" {
			if len(event.Item.Text) > 64<<10 {
				return true, r.ClosePrediction(p)
			}
			p.Text = event.Item.Text
		}
	case "turn/completed":
		if p.Turn != "" && event.Turn.ID != p.Turn {
			return true, nil
		}
		p.Completed = true
		if event.Turn.Status != "completed" {
			p.Closed = true
		}
		if !p.Closed {
			var answer struct {
				Suggestion *string `json:"suggestion"`
			}
			if json.Unmarshal([]byte(p.Text), &answer, json.RejectUnknownMembers(true)) != nil || answer.Suggestion == nil {
				p.Text = ""
			} else {
				p.Text = strings.TrimSpace(*answer.Suggestion)
			}
		}
		// Completion retains its result but releases the ephemeral host resource.
		return true, r.releasePrediction(p)
	case "thread/closed":
		p.release.HostClosed()
		p.release.Complete()
		if !p.Completed {
			p.Closed = true
		}
		r.sidesMu.Lock()
		delete(r.predictions, event.ThreadID)
		r.sidesMu.Unlock()
		c.RetireThreads(event.ThreadID)
	}
	return true, nil
}
