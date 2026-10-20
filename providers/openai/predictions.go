package openai

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PredictionSettings is the remote-authored desktop composer contract.
// Source: ChatGPT desktop 26.1013.40934, main-CLu1A25j.js C_e/w_e/b_e.
type PredictionSettings struct {
	Enabled           bool             `json:"is_enabled"`
	UnsupportedModels []string         `json:"unsupported_models"`
	SampleRate        *float64         `json:"sample_rate"`
	LiveSampleRate    *float64         `json:"live_sample_rate"`
	CatchUpSampleRate *float64         `json:"catch_up_sample_rate"`
	Prompt            string           `json:"prompt"`
	Effort            string           `json:"prediction_reasoning_effort"`
	Policy            PredictionPolicy `json:"-"`
}

func (s PredictionSettings) Rate(trigger string) float64 {
	rate := s.LiveSampleRate
	if trigger == "catch_up" {
		rate = s.CatchUpSampleRate
	}
	if rate == nil {
		rate = s.SampleRate
	}
	if rate == nil {
		return 1
	}
	return *rate
}

type PredictionPolicy struct {
	Thresholds      map[string]float64 `json:"st"`
	UserLabels      []string           `json:"ubl"`
	AssistantLabels []string           `json:"abl"`
}

func (p PredictionPolicy) Suppresses(metadata jsontext.Value) bool {
	type result struct {
		Labels []string           `json:"labels"`
		Scores map[string]float64 `json:"_modapi_scores"`
	}
	type output struct {
		Results []result `json:"results"`
	}
	type provider struct {
		Outputs []output `json:"outputs"`
	}
	var data struct {
		Prompt     map[string]provider `json:"prompt"`
		Generation map[string]provider `json:"generation"`
	}
	if json.Unmarshal(metadata, &data) != nil {
		return false
	}
	for source, providers := range []map[string]provider{data.Prompt, data.Generation} {
		labels := p.UserLabels
		if source == 1 {
			labels = p.AssistantLabels
		}
		for _, provider := range providers {
			for _, output := range provider.Outputs {
				for _, result := range output.Results {
					for _, label := range result.Labels {
						if slices.Contains(labels, label) {
							return true
						}
					}
					if source == 0 {
						for name, threshold := range p.Thresholds {
							if score, ok := result.Scores[name]; ok && score >= threshold {
								return true
							}
						}
					}
				}
			}
		}
	}
	return false
}

type predictionSettingsCache struct {
	sync.Mutex
	authorization, account string
	version                uint64
	expires                time.Time
	settings               *PredictionSettings
}

func (c *predictionSettingsCache) observe(headers http.Header) {
	authorization, account, err := Credentials(headers)
	if err != nil {
		return
	}
	c.Lock()
	defer c.Unlock()
	if authorization != c.authorization || account != c.account {
		c.authorization, c.account = authorization, account
		c.version++
		c.settings, c.expires = nil, time.Time{}
	}
}

// PredictionIdentity changes when the authenticated request snapshot changes.
// Zero means no authenticated provider request has supplied credentials yet.
func (c *Client) PredictionIdentity() uint64 {
	if c == nil {
		return 0
	}
	c.predictions.Lock()
	defer c.predictions.Unlock()
	return c.predictions.version
}

func (c *Client) PredictionSettings(ctx context.Context) (*PredictionSettings, error) {
	parent := ctx
	c.predictions.Lock()
	version, authorization, account := c.predictions.version, c.predictions.authorization, c.predictions.account
	settings, expires := c.predictions.settings, c.predictions.expires
	c.predictions.Unlock()
	if version == 0 {
		return nil, errors.New("prediction authentication unavailable")
	}
	if time.Now().Before(expires) {
		return settings, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var config PredictionSettings
	if err := c.predictionJSON(ctx, authorization, account, "/wham/predictions/config", nil, &config); err != nil {
		return nil, err
	}
	for _, rate := range []*float64{config.SampleRate, config.LiveSampleRate, config.CatchUpSampleRate} {
		if rate != nil && (*rate < 0 || *rate > 1) {
			return nil, errors.New("invalid prediction sampling rate")
		}
	}
	if config.Enabled && strings.TrimSpace(config.Prompt) == "" {
		return nil, errors.New("invalid prediction prompt configuration")
	}
	if config.Enabled {
		// The authenticated bootstrap evaluates identity server-side. Retain only
		// this policy; do not adopt execution assignments or emit Statsig events.
		// Source: ChatGPT desktop 26.1013.40934, app-initial-a68277f60b93.js tWl/Vya.
		body, _ := json.Marshal(map[string]string{"window_type": "electron", "brand_name": "chatgpt", "system_name": runtime.GOOS})
		var bootstrap struct {
			Payload string `json:"statsigPayload"`
		}
		if c.predictionJSON(ctx, authorization, account, "/wham/statsig/bootstrap", body, &bootstrap) == nil {
			var values struct {
				Configs map[string]struct {
					Value jsontext.Value `json:"value"`
				} `json:"dynamic_configs"`
			}
			if json.Unmarshal([]byte(bootstrap.Payload), &values) == nil {
				const name = "codex-app-cumberbatch-mod-policy"
				// Statsig's default DJB2 name encoding uses a forward 31x hash.
				var hash uint32
				for _, char := range name {
					hash = hash*31 + uint32(char)
				}
				entry, found := values.Configs[name]
				if !found {
					entry = values.Configs[strconv.FormatUint(uint64(hash), 10)]
				}
				var policy PredictionPolicy
				if json.Unmarshal(entry.Value, &policy) == nil {
					config.Policy = policy
				}
			}
		}
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	c.predictions.Lock()
	defer c.predictions.Unlock()
	if c.predictions.version != version {
		return nil, errors.New("prediction account changed")
	}
	c.predictions.settings, c.predictions.expires = &config, time.Now().Add(5*time.Minute)
	return &config, nil
}

func (c *Client) predictionJSON(ctx context.Context, authorization, account, path string, body []byte, out any) error {
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	base := strings.TrimSuffix(strings.TrimRight(c.baseURL, "/"), "/codex")
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set(AccountIDHeader, account)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("prediction configuration rejected (%d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errors.New("prediction configuration exceeds 8 MiB")
	}
	return json.Unmarshal(data, out)
}
