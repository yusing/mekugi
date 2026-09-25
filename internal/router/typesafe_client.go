package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	typesafeEndpoint = "https://api.typesafe.ai/v1/systemone"
	// Pin a version: filter thresholds were tuned against it, and an alias
	// would move them without a change here.
	typesafeModel       = "jev-1.13.0"
	typesafeMaxAttempts = 3
)

// typesafeClient asks TypeSafe System One questions. Its credential and
// transport are separate from every Responses provider.
type typesafeClient struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
	model      string
}

type typesafeNoul struct {
	Type         string         `json:"type"`
	Instructions any            `json:"instructions"`
	Criteria     map[string]any `json:"criteria,omitempty"`
}

type typesafeUsage struct {
	InputTokens      uint64 `json:"input_tokens"`
	OutputTokens     uint64 `json:"output_tokens"`
	Requests         uint64 `json:"requests"`
	MissingResponses uint64 `json:"missing_responses"`
	Incomplete       bool   `json:"incomplete,omitzero"`
}

func (u *typesafeUsage) add(next typesafeUsage) {
	if u.Incomplete || next.Incomplete {
		u.Incomplete = true
		return
	}
	sum := *u
	for _, pair := range []struct {
		dst *uint64
		add uint64
	}{
		{&sum.InputTokens, next.InputTokens}, {&sum.OutputTokens, next.OutputTokens},
		{&sum.Requests, next.Requests}, {&sum.MissingResponses, next.MissingResponses},
	} {
		if ^uint64(0)-*pair.dst < pair.add {
			u.Incomplete = true
			return
		}
		*pair.dst += pair.add
	}
	*u = sum
}

func newTypesafeClient(apiKey string) *typesafeClient {
	client := withDialTimeout(nil)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &typesafeClient{httpClient: client, endpoint: typesafeEndpoint, apiKey: apiKey, model: typesafeModel}
}

// nouls returns the yes probability of every question. A partial answer set
// is an error: callers must not treat a missing judgment as a no.
func (c *typesafeClient) nouls(ctx context.Context, state any, questions map[string]typesafeNoul) (map[string]float64, typesafeUsage, error) {
	body, err := jsonv2.Marshal(map[string]any{"model": c.model, "state": state, "questions": questions})
	if err != nil {
		return nil, typesafeUsage{}, err
	}
	var response struct {
		Answers jsontext.Value `json:"answers"`
		Usage   *struct {
			InputTokens  *uint64 `json:"input_tokens"`
			OutputTokens *uint64 `json:"output_tokens"`
		} `json:"usage"`
	}
	var usage typesafeUsage
	for attempt := 1; ; attempt++ {
		response.Usage = nil
		response.Answers = nil
		retryAfter, err := c.post(ctx, body, &response)
		observed := typesafeUsage{Requests: 1}
		if err == nil && response.Usage != nil && response.Usage.InputTokens != nil && response.Usage.OutputTokens != nil {
			observed.InputTokens, observed.OutputTokens = *response.Usage.InputTokens, *response.Usage.OutputTokens
		} else {
			observed.MissingResponses = 1
		}
		usage.add(observed)
		if err == nil {
			break
		}
		if retryAfter < 0 || attempt == typesafeMaxAttempts {
			return nil, usage, err
		}
		select {
		case <-ctx.Done():
			return nil, usage, errors.Join(err, ctx.Err())
		case <-time.After(retryAfter):
		}
	}
	// Answer schema failures must not discard independently usable usage.
	var decoded map[string]struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	}
	if jsonv2.Unmarshal(response.Answers, &decoded) != nil {
		return nil, usage, errors.New("typesafe: invalid answers")
	}
	answers := make(map[string]float64, len(questions))
	for id := range questions {
		answer, ok := decoded[id]
		if !ok || answer.Type != "noul" || answer.Noul == nil || *answer.Noul < 0 || *answer.Noul > 1 {
			return nil, usage, fmt.Errorf("typesafe: missing or invalid answer %q", id)
		}
		answers[id] = *answer.Noul
	}
	return answers, usage, nil
}

// post returns a non-negative retry delay only for retryable failures.
func (c *typesafeClient) post(ctx context.Context, body []byte, response any) (time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return -1, err
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "mekugi")
	reply, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return -1, err
		}
		return 200 * time.Millisecond, err
	}
	defer reply.Body.Close()
	data, err := io.ReadAll(io.LimitReader(reply.Body, 1<<20))
	if err != nil {
		return -1, err
	}
	switch {
	case reply.StatusCode == http.StatusOK:
		return -1, jsonv2.Unmarshal(data, response)
	case reply.StatusCode == http.StatusTooManyRequests || reply.StatusCode == 529 || reply.StatusCode >= 500:
		delay := 300 * time.Millisecond
		if seconds, err := strconv.ParseFloat(reply.Header.Get("Retry-After"), 64); err == nil && seconds >= 0 {
			delay = min(time.Duration(seconds*float64(time.Second)), 2*time.Second)
		}
		return delay, fmt.Errorf("typesafe: HTTP %d", reply.StatusCode)
	default:
		// Provider error bodies may echo request content or credentials. Keep
		// the error safe for the filter's sanitized debug event.
		return -1, fmt.Errorf("typesafe: HTTP %d", reply.StatusCode)
	}
}
