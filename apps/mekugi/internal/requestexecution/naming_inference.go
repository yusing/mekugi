// Source: internal/router/session_title_generate.go:107:238@[e2ee92a9] inference adapter.
package requestexecution

import (
	"context"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/modules/usage/accounting"

	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inference"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/openai"
)

// NamingCompletion captures only authentication for one auxiliary title operation.
func NamingCompletion(provider inferencegateway.Forwarder, totals *accounting.Service, thread string, headers http.Header) inference.Complete {
	if provider == nil {
		return nil
	}
	authorization, accountID, err := openai.Credentials(headers)
	if err != nil {
		return nil
	}
	auxHeaders := http.Header{}
	auxHeaders.Set("Authorization", authorization)
	auxHeaders.Set(openai.AccountIDHeader, accountID)
	return func(ctx context.Context, request inference.Request) (string, error) {
		body, err := json.Marshal(map[string]any{
			"model": request.Model, "reasoning": map[string]string{"effort": request.Effort},
			"stream": true, "store": false, "tool_choice": "auto", "parallel_tool_calls": false, "include": []string{},
			"instructions": request.Instructions,
			"input":        []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": request.Input}}}},
		})
		if err != nil {
			return "", err
		}
		response, err := provider.ForwardExecution(ctx, ctx, body, auxHeaders, "")
		var usage *accounting.Observation
		_, rejected := errors.AsType[*inferencegateway.ProviderHTTPError](err)
		if totals != nil && !rejected && (response == nil || response.StatusCode >= 200 && response.StatusCode < 300) {
			usage = totals.Observation(thread, thread, request.Model, "")
			usage.Reasoning = request.Effort
			defer usage.Finish()
		}
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return "", errors.New("session title request rejected by upstream")
		}
		stream, err := codexresponses.PrepareBody(response.Response, true)
		if err != nil {
			return "", err
		}
		collector := new(sessionTitleCollector)
		hooks := &ResponseHooks{Transport: response.Transport}
		if usage != nil {
			hooks.OnUsage = usage.Observe
		}
		state, err := CopyResponse(io.Discard, response.Response, stream, collector, hooks)
		if err != nil {
			return "", err
		}
		if state != responseevents.TerminalCompleted {
			return "", errors.New("session title request did not complete")
		}
		return collector.text, nil
	}
}

type sessionTitleCollector struct{ text string }

func (c *sessionTitleCollector) TransformJSON(payload []byte) ([]byte, error) {
	var response struct {
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	var text strings.Builder
	for _, item := range response.Output {
		if item.Type == "message" && item.Role == "assistant" {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		}
	}
	if text.Len() > 0 {
		c.text = text.String()
	}
	return payload, nil
}

func (c *sessionTitleCollector) TransformSSE(payload []byte) ([][]byte, error) {
	var event struct {
		Type     string         `json:"type"`
		Delta    string         `json:"delta"`
		Response jsontext.Value `json:"response"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, err
	}
	if event.Type == "response.output_text.delta" {
		c.text += event.Delta
	}
	if event.Type == "response.completed" {
		if _, err := c.TransformJSON(event.Response); err != nil {
			return nil, err
		}
	}
	return [][]byte{payload}, nil
}

func (*sessionTitleCollector) Finish(bool) error { return nil }
