//go:build e2e

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const cacheAffinityE2EPrompt = `Call the exec tool exactly 12 times, sequentially and never in parallel. Call 1 must run printf 'probe 1\n', call 2 must run printf 'probe 2\n', and continue through call 12 running printf 'probe 12\n'. Do not combine calls. Do not edit any file. After the twelfth tool result, answer only: done`

type cacheAffinityE2ETransport struct {
	delegate http.RoundTripper

	mu              sync.Mutex
	usages          []tokenCounts
	promptCacheKeys []string
	headers         []http.Header
}

func (t *cacheAffinityE2ETransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	if err := request.Body.Close(); err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	var promptCacheKey string
	if raw := fields["prompt_cache_key"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &promptCacheKey); err != nil {
			return nil, err
		}
	}

	t.mu.Lock()
	t.promptCacheKeys = append(t.promptCacheKeys, promptCacheKey)
	t.headers = append(t.headers, request.Header.Clone())
	t.mu.Unlock()

	response, err := t.delegate.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = &cacheAffinityE2EBody{
		ReadCloser: response.Body,
		record:     t.recordResponse,
	}
	return response, nil
}

func (t *cacheAffinityE2ETransport) recordResponse(body []byte) {
	var usage tokenCounts
	var found bool
	for _, line := range strings.Split(string(body), "\n") {
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		if counts, ok := usageFromResponsePayload([]byte(strings.TrimSpace(payload)), true); ok {
			usage = counts
			found = true
		}
	}
	if !found {
		if counts, ok := usageFromResponsePayload(body, false); ok {
			usage = counts
			found = true
		}
	}
	if found {
		t.mu.Lock()
		t.usages = append(t.usages, usage)
		t.mu.Unlock()
	}
}

func (t *cacheAffinityE2ETransport) snapshot() ([]tokenCounts, []string, []http.Header) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]tokenCounts(nil), t.usages...),
		append([]string(nil), t.promptCacheKeys...),
		append([]http.Header(nil), t.headers...)
}

type cacheAffinityE2EBody struct {
	io.ReadCloser
	once   sync.Once
	buffer bytes.Buffer
	record func([]byte)
}

func (b *cacheAffinityE2EBody) Read(target []byte) (int, error) {
	count, err := b.ReadCloser.Read(target)
	b.buffer.Write(target[:count])
	if errors.Is(err, io.EOF) {
		b.finish()
	}
	return count, err
}

func (b *cacheAffinityE2EBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish()
	return err
}

func (b *cacheAffinityE2EBody) finish() {
	b.once.Do(func() {
		b.record(bytes.Clone(b.buffer.Bytes()))
	})
}

func TestCodexCacheAffinityE2E(t *testing.T) {
	codexPath := requireExecutable(t, "codex")
	gitPath := requireExecutable(t, "git")
	upstreamTransport := http.DefaultTransport.(*http.Transport).Clone()
	defer upstreamTransport.CloseIdleConnections()
	transport := &cacheAffinityE2ETransport{delegate: upstreamTransport}

	httpClient := &http.Client{Transport: transport}
	var requestSequence atomic.Uint64
	handler := responsesHandler(
		t.Context(),
		10*time.Minute,
		newProviderClient(codexBaseURL, httpClient),
		nil,
		newManagedMekugiProxy(t),
		nil, nil,
		&requestSequence,
	)
	var (
		inboundMu      sync.Mutex
		inboundHeaders []http.Header
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
			http.NotFound(writer, request)
			return
		}
		inboundMu.Lock()
		inboundHeaders = append(inboundHeaders, request.Header.Clone())
		inboundMu.Unlock()
		handler(writer, request)
	}))
	defer server.Close()

	workspace := t.TempDir()
	command := exec.Command(gitPath, "init", "--quiet")
	command.Dir = workspace
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize probe repository: %v\n%s", err, output)
	}

	model := environmentOrDefault("MEKUGI_E2E_MODEL", "gpt-5.6-sol")
	providerName := "cache-affinity-e2e"
	providerConfig := "model_providers." + providerName + "={ name = " + strconv.Quote(providerName) +
		", base_url = " + strconv.Quote(server.URL+"/v1") + ", wire_api = \"responses\", requires_openai_auth = true }"
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	args := []string{
		"-c", providerConfig,
		"--local-provider", providerName,
		"--oss",
		"--model", model,
		"--sandbox", "read-only",
		"--ask-for-approval", "never",
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--json",
		"--color", "never",
		"-C", workspace,
		cacheAffinityE2EPrompt,
	}
	var stdout, stderr bytes.Buffer
	codex := exec.CommandContext(ctx, codexPath, args...)
	codex.Stdout = &stdout
	codex.Stderr = &stderr
	if err := codex.Run(); err != nil {
		t.Fatalf(
			"run Codex cache-affinity E2E: %v\nstdout tail:\n%s\nstderr tail:\n%s",
			err,
			tailBytes(stdout.Bytes(), 64<<10),
			tailBytes(stderr.Bytes(), 64<<10),
		)
	}

	usages, keys, forwardedHeaders := transport.snapshot()
	inboundMu.Lock()
	capturedInbound := append([]http.Header(nil), inboundHeaders...)
	inboundMu.Unlock()
	if len(usages) < 8 {
		t.Fatalf("captured %d upstream usage samples, want at least 8\nstdout:\n%s", len(usages), stdout.Bytes())
	}
	if len(capturedInbound) == 0 {
		t.Fatal("captured no incoming Responses request")
	}
	expectedHeaders := capturedInbound[0]
	codexRequestHeaderNames := []string{
		threadIDHeader,
		clientRequestIDHeader,
		codexWindowIDHeader,
		codexBetaFeaturesHeader,
		codexResponsesLiteHeader,
		codexTurnMetadataHeader,
	}
	for _, name := range codexRequestHeaderNames {
		if len(expectedHeaders.Values(name)) == 0 {
			t.Fatalf("Codex did not send request header %s", name)
		}
	}
	for index, headers := range capturedInbound {
		if _, _, err := requiredCodexAuthHeaders(headers); err != nil {
			t.Fatalf("incoming request %d has invalid Codex authentication: %v", index+1, err)
		}
		for _, name := range codexRequestHeaderNames {
			if got, want := headers.Values(name), expectedHeaders.Values(name); !slices.Equal(got, want) {
				t.Fatalf("incoming request %d header %s = %q, want stable %q", index+1, name, got, want)
			}
		}
	}
	if len(keys) == 0 || strings.TrimSpace(keys[0]) == "" {
		t.Fatal("Codex did not send prompt_cache_key")
	}
	for index, key := range keys {
		if key != keys[0] {
			t.Fatalf("prompt_cache_key changed at upstream attempt %d: %q != %q", index+1, key, keys[0])
		}
	}
	for index, headers := range forwardedHeaders {
		if _, _, err := requiredCodexAuthHeaders(headers); err != nil {
			t.Fatalf("upstream attempt %d has invalid Codex authentication: %v", index+1, err)
		}
		if got := headers.Get(codexSessionIDHeader); got != keys[index] {
			t.Fatalf("upstream attempt %d Session_id = %q, want prompt_cache_key %q", index+1, got, keys[index])
		}
		if got := headers.Get(sessionIDHeader); got != "" {
			t.Fatalf("upstream attempt %d Session-Id = %q, want empty", index+1, got)
		}
		for _, name := range codexRequestHeaderNames {
			if got, want := headers.Values(name), expectedHeaders.Values(name); !slices.Equal(got, want) {
				t.Fatalf("upstream attempt %d header %s = %q, want incoming %q", index+1, name, got, want)
			}
		}
	}

	var totalInput, totalCached, warmInput, warmCached uint64
	var warmFullMisses int
	for index, usage := range usages {
		cached := usage.InputTokens - usage.UncachedInputTokens
		totalInput += usage.InputTokens
		totalCached += cached
		if index > 0 {
			warmInput += usage.InputTokens
			warmCached += cached
			if cached == 0 {
				warmFullMisses++
			}
		}

		t.Logf(
			"request %02d: input=%d cached=%d uncached=%d",
			index+1,
			usage.InputTokens,
			cached,
			usage.UncachedInputTokens,
		)
	}
	totalRatio := float64(totalCached) / float64(totalInput)
	warmRatio := float64(warmCached) / float64(warmInput)
	t.Logf("cache ratio: %.2f%% (%d/%d input tokens)", 100*totalRatio, totalCached, totalInput)
	t.Logf("warm cache ratio: %.2f%% (%d/%d input tokens)", 100*warmRatio, warmCached, warmInput)
	t.Logf("warm full misses: %d/%d requests", warmFullMisses, len(usages)-1)
}
