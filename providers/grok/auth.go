package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: internal/router/grok_auth.go:20:251@[18475aaf] authentication.
const (
	grokIssuer        = "https://auth.x.ai"
	grokOAuthClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	grokProxyEndpoint = "https://cli-chat-proxy.grok.com/v1/chat/completions"
	grokAPIEndpoint   = "https://api.x.ai/v1/chat/completions"
	grokAuthLimit     = 1 << 20
)

type Auth struct {
	path       string
	apiKey     string
	httpClient *http.Client
}
type Credentials struct {
	Endpoint string
	Headers  http.Header
}

// NewAuth copies the invocation's transport and enforces credential redirect policy.
func NewAuth(path, apiKey string, transport *http.Client) *Auth {
	client := *transport
	// Neither inference nor OIDC credentials may follow a redirect to a new host.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Auth{path: path, apiKey: strings.TrimSpace(apiKey), httpClient: &client}
}
func (a *Auth) Credentials(ctx context.Context) (Credentials, error) {
	if a.apiKey != "" {
		return Credentials{Endpoint: grokAPIEndpoint, Headers: http.Header{"Authorization": []string{"Bearer " + a.apiKey}, "User-Agent": []string{"mekugi"}}}, nil
	}
	_, entry, err := a.read()
	if err != nil {
		return Credentials{}, err
	}
	expiry, err := time.Parse(time.RFC3339Nano, responses.StringField(entry, "expires_at"))
	if err != nil {
		return Credentials{}, errors.New("grok OAuth expiry is invalid; run grok login")
	}
	if !expiry.After(time.Now().Add(time.Minute)) {
		entry, err = a.refresh(ctx, "")
		if err != nil {
			return Credentials{}, err
		}
	}
	token := responses.StringField(entry, "key")
	if token == "" {
		return Credentials{}, errors.New("grok access token is missing; run grok login")
	}
	return Credentials{Endpoint: grokProxyEndpoint, Headers: http.Header{
		"Authorization": []string{"Bearer " + token}, "X-Xai-Token-Auth": []string{"xai-grok-cli"},
		"X-Grok-Client-Identifier": []string{"mekugi"},
		// The proxy gates its CLI wire protocol independently of our user agent.
		"X-Grok-Client-Version": []string{"1.0.13"}, "X-Grok-Client-Mode": []string{"headless"}, "User-Agent": []string{"mekugi"},
	}}, nil
}
func (a *Auth) read() (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	info, err := os.Lstat(a.path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("grok credential store must be an existing regular file; run grok login or set XAI_API_KEY")
	}
	file, err := os.Open(a.path)
	if err != nil {
		return nil, nil, errors.New("cannot read Grok credentials; run grok login or set XAI_API_KEY")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, grokAuthLimit+1))
	if err != nil || len(data) > grokAuthLimit {
		return nil, nil, errors.New("cannot read bounded Grok credential store")
	}
	var store map[string]json.RawMessage
	if json.Unmarshal(data, &store) != nil {
		return nil, nil, errors.New("invalid Grok credential store")
	}
	var entry map[string]json.RawMessage
	if json.Unmarshal(store[grokIssuer+"::"+grokOAuthClientID], &entry) != nil || entry == nil {
		return nil, nil, errors.New("grok OAuth login is missing; run grok login --oauth or set XAI_API_KEY")
	}
	if responses.StringField(entry, "auth_mode") != "oidc" || responses.StringField(entry, "oidc_issuer") != grokIssuer || responses.StringField(entry, "oidc_client_id") != grokOAuthClientID {
		return nil, nil, errors.New("unsupported Grok OAuth credential issuer or client")
	}
	return store, entry, nil
}

func (a *Auth) refresh(ctx context.Context, rejectedToken string) (map[string]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lockPath := filepath.Join(filepath.Dir(a.path), "auth.json.lock")
	lock := flock.New(lockPath)
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil || !locked {
		return nil, errors.Join(ctx.Err(), errors.New("could not acquire Grok credential refresh lock"))
	}
	// Grok's advisory lock is never removed. Keep its PID:timestamp stamp fresh
	// for older Grok versions which use that stamp to detect stale holders.
	held, err := lock.Stat()
	if err != nil {
		return nil, errors.New("cannot inspect Grok refresh lock")
	}
	current, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(held, current) {
		return nil, errors.New("grok refresh lock was replaced")
	}
	stamp, err := os.OpenFile(lockPath, os.O_WRONLY, 0)
	if err != nil {
		return nil, errors.New("cannot update Grok refresh lock")
	}
	defer stamp.Close()
	writeStamp := func() {
		_ = stamp.Truncate(0)
		_, _ = stamp.WriteAt([]byte(fmt.Sprintf("%d:%d", os.Getpid(), time.Now().Unix())), 0)
		_ = stamp.Sync()
	}
	writeStamp()
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeStamp()
			case <-stop:
				return
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	// Re-read after locking: another CLI/router may already have refreshed or
	// changed other accounts. Preserve every unrelated field on writeback.
	store, entry, err := a.read()
	if err != nil {
		return nil, err
	}
	expiry, err := time.Parse(time.RFC3339Nano, responses.StringField(entry, "expires_at"))
	if err != nil {
		return nil, errors.New("invalid Grok OAuth expiry")
	}
	if expiry.After(time.Now().Add(time.Minute)) && (rejectedToken == "" || responses.StringField(entry, "key") != rejectedToken) {
		return entry, nil
	}
	refresh := responses.StringField(entry, "refresh_token")
	if refresh == "" {
		return nil, errors.New("grok refresh token is missing; run grok login")
	}
	discovery, err := http.NewRequestWithContext(ctx, http.MethodGet, grokIssuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	var metadata struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := a.authJSON(discovery, &metadata); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(metadata.TokenEndpoint)
	if err != nil || metadata.Issuer != grokIssuer || endpoint.Scheme != "https" || endpoint.Host != "auth.x.ai" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("untrusted Grok OAuth token endpoint")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {grokOAuthClientID}}
	for _, key := range []string{"principal_type", "principal_id"} {
		if value := responses.StringField(entry, key); value != "" {
			form.Set(key, value)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := a.authJSON(request, &tokens); err != nil {
		return nil, err
	}
	if tokens.AccessToken == "" || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > int64((365*24*time.Hour)/time.Second) {
		return nil, errors.New("invalid Grok OAuth refresh response; run grok login")
	}
	entry["key"] = responses.MustMarshalJSON(tokens.AccessToken)
	if tokens.RefreshToken != "" {
		entry["refresh_token"] = responses.MustMarshalJSON(tokens.RefreshToken)
	}
	entry["expires_at"] = responses.MustMarshalJSON(time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UTC().Format(time.RFC3339Nano))
	store[grokIssuer+"::"+grokOAuthClientID] = responses.MustMarshalJSON(entry)
	if err := writeGrokCredentials(a.path, responses.MustMarshalJSON(store)); err != nil {
		return nil, err
	}
	return entry, nil
}
func (a *Auth) authJSON(request *http.Request, target any) error {
	response, err := a.httpClient.Do(request)
	if err != nil {
		return errors.Join(request.Context().Err(), errors.New("grok OAuth request failed"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("grok OAuth returned HTTP %d; retry or run grok login", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, grokAuthLimit+1))
	if err != nil || len(data) > grokAuthLimit || json.Unmarshal(data, target) != nil {
		return errors.New("invalid Grok OAuth response")
	}
	return nil
}
func writeGrokCredentials(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("grok credential store is not a regular file")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mekugi-grok-auth-")
	if err != nil {
		return errors.New("cannot prepare Grok credential update")
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return errors.New("cannot save refreshed Grok credentials; run grok login")
	}
	if err = os.Rename(name, path); err != nil {
		return errors.New("cannot commit refreshed Grok credentials; run grok login")
	}
	return nil
}

func (a *Auth) UsesAPIKey() bool { return a.apiKey != "" }

// Refresh retries rejected credentials without exposing private store records.
func (a *Auth) Refresh(ctx context.Context, rejectedToken string) error {
	_, err := a.refresh(ctx, rejectedToken)
	return err
}
