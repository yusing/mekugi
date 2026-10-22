package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/providers/openai"
)

type predictionRequest struct {
	thread, turn, trigger string
	revision, identity    uint64
	deadline              time.Time
	cancel                context.CancelFunc
	operation             *sessioncontrol.Prediction
	settings              *openai.PredictionSettings
}

type predictionConfiguration struct {
	request  *predictionRequest
	settings *openai.PredictionSettings
}

// composerPredictions is invocation-local asynchronous acquisition. Host work
// stays on Session Control's one ledger, draft eligibility stays on its revision.
type composerPredictions struct {
	provider              *openai.Client
	results               chan predictionConfiguration
	thread, turn, trigger string
	observedTurn          string
	metadata              []jsontext.Value
	suppressed            bool
	attempted, disabled   bool
	supported             bool
	request               *predictionRequest
}

func (p *composerPredictions) observe(m appserver.Message, main string) {
	if p.provider == nil {
		return
	}
	if p.thread != main {
		p.thread, p.turn, p.observedTurn, p.metadata, p.attempted = main, "", "", nil, false
		p.suppressed = false
	}
	if m.Method == "" && m.Error == nil {
		var result struct {
			Thread binding.ThreadInfo `json:"thread"`
		}
		if json.Unmarshal(m.Result, &result) == nil && result.Thread.ID == main && len(result.Thread.Turns) > 0 {
			last := result.Thread.Turns[len(result.Thread.Turns)-1]
			if p.observedTurn != last.ID {
				p.observedTurn, p.metadata, p.suppressed = last.ID, nil, false
			}
			if last.Status == "completed" && p.turn != last.ID {
				p.turn, p.trigger, p.attempted = last.ID, "catch_up", false
			}
		}
		return
	}
	if m.Method != "turn/started" && m.Method != "turn/completed" && m.Method != "turn/moderationMetadata" {
		return
	}
	var event struct {
		binding.Notification
		Metadata jsontext.Value `json:"metadata"`
	}
	if json.Unmarshal(m.Params, &event) != nil || event.ThreadID != main {
		return
	}
	switch m.Method {
	case "turn/started":
		p.observedTurn, p.metadata = event.Turn.ID, nil
		p.suppressed = false
		p.turn, p.attempted = "", false
	case "turn/moderationMetadata":
		if event.TurnID == p.observedTurn && !p.suppressed {
			if len(p.metadata) >= 64 || len(event.Metadata) > 64<<10 {
				// Incomplete moderation evidence cannot enable a suggestion.
				p.suppressed, p.metadata = true, nil
			} else {
				p.metadata = append(p.metadata, append(jsontext.Value(nil), event.Metadata...))
			}
		}
	case "turn/completed":
		if event.Turn.Status == "completed" && event.Turn.ID != p.turn {
			p.turn, p.trigger, p.attempted = event.Turn.ID, "live", false
		}
	}
}

func (p *composerPredictions) clear(control *sessioncontrol.Client[appServerRequest], conversation *conversationmodel.Session) error {
	conversation.Prediction = conversationmodel.Prediction{}
	request := p.request
	p.request = nil
	if request == nil {
		return nil
	}
	request.cancel()
	return control.Requests.ClosePrediction(request.operation)
}

func (p *composerPredictions) sync(ctx context.Context, attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], restoring, focused, disabled bool) error {
	if p.provider == nil {
		return nil
	}
	draft := attached.conversation.DraftView()
	if p.disabled != disabled {
		p.disabled, p.attempted = disabled, false
		if err := p.clear(control, attached.conversation); err != nil {
			return err
		}
	}
	identity := p.provider.PredictionIdentity()
	available := p.supported && !disabled && !restoring && draft.Available && draft.Draft.Text == "" && control.Thread == p.thread && p.turn != "" && !control.Busy(len(attached.conversation.Unsent)+len(attached.conversation.Queued) > 0)
	if request := p.request; request != nil {
		if !available || request.thread != p.thread || request.turn != p.turn || request.revision != draft.Revision || request.identity != identity || time.Now().After(request.deadline) && (request.operation == nil || !request.operation.Completed) {
			return p.clear(control, attached.conversation)
		}
		if request.settings != nil {
			if slices.ContainsFunc(p.metadata, request.settings.Policy.Suppresses) {
				p.suppressed = true
			}
			p.metadata = nil
		}
		if operation := request.operation; operation != nil && operation.Completed && !operation.Closed {
			text := operation.Text
			if p.suppressed {
				text = ""
			}
			attached.conversation.Prediction = conversationmodel.Prediction{Text: text, Revision: request.revision}
		}
	}
	if !available || !focused || p.attempted || p.request != nil || identity == 0 || control.Requests.PredictionPending() {
		return nil
	}
	p.attempted = true
	requestCtx, cancel := context.WithCancel(ctx)
	request := &predictionRequest{thread: p.thread, turn: p.turn, trigger: p.trigger, revision: draft.Revision, identity: identity, deadline: time.Now().Add(30 * time.Second), cancel: cancel}
	p.request = request
	go func() {
		settings, err := p.provider.PredictionSettings(requestCtx)
		if err != nil {
			settings = nil
		}
		select {
		case p.results <- predictionConfiguration{request, settings}:
		case <-requestCtx.Done():
		}
	}()
	return nil
}

func (p *composerPredictions) configured(result predictionConfiguration, control *sessioncontrol.Client[appServerRequest]) error {
	request, settings := result.request, result.settings
	if p.request != request || settings == nil || !settings.Enabled || rand.Float64() >= settings.Rate(request.trigger) {
		return nil
	}
	request.settings = settings
	request.operation = &sessioncontrol.Prediction{SourceThread: request.thread, SourceTurn: request.turn, Trigger: request.trigger, Prompt: settings.Prompt + "\n\nReturn only JSON matching this schema: {\"type\":\"object\",\"properties\":{\"suggestion\":{\"type\":[\"string\",\"null\"]}},\"required\":[\"suggestion\"],\"additionalProperties\":false}.", Effort: settings.Effort, UnsupportedModels: settings.UnsupportedModels}
	return control.Requests.StartPrediction(request.operation)
}
