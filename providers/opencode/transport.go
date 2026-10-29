package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/yusing/mekugi/libraries/httptransport"
)

// Source: internal/router/grok_client.go:48:64,90:109@8cd86549 authenticated inference.

// Forward sends an encoded request in the selected format without retries or
// redirects. Only this service's credentials and scoped affinity are attached.
func (s Service) Forward(ctx context.Context, supplied *http.Client, format, thread string, body []byte) (*http.Response, error) {
	endpoint := s.Endpoint
	headers := http.Header{"Authorization": {"Bearer " + s.APIKey}, "User-Agent": {"mekugi"}}
	if session := sessionID(s.Prefix, thread); session != "" {
		headers.Set("x-opencode-session", session)
	}
	switch format {
	case "anthropic":
		endpoint = strings.TrimSuffix(endpoint, "/chat/completions") + "/messages"
		headers.Del("Authorization")
		headers.Set("x-api-key", s.APIKey)
		headers.Set("anthropic-version", "2023-06-01")
	case "responses":
		endpoint = strings.TrimSuffix(endpoint, "/chat/completions") + "/responses"
	}
	request, err := httptransport.NewRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("invalid %s endpoint", s.Label)
	}
	request.Header = headers
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	client := httptransport.Client(supplied)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, &httptransport.SendError{Err: fmt.Errorf("%s request failed: %w", s.Label, err)}
	}
	return response, nil
}

// sessionID is stable across restarts and isolated by service and thread.
// Source: internal/router/opencode.go:9:15@8cd86549 openCodeSessionID.
func sessionID(provider, thread string) string {
	if strings.TrimSpace(thread) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(provider + "\x00" + strings.TrimSpace(thread)))
	return "mekugi-" + hex.EncodeToString(sum[:])
}
