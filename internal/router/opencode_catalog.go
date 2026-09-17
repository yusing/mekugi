package router

import (
	"context"
	"embed"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const openCodeCatalogTTL = time.Hour
const openCodeCatalogLimit = 16 << 20

// Immutable snapshots are shared by catalog rendering, routing and price lookup.
// No credentials, account data or provider response content enter this cache.
type openCodeSnapshot struct {
	Version int                                    `json:"version"`
	Updated time.Time                              `json:"updated"`
	Models  map[string]map[string]openCodeMetadata `json:"models"`
}

type openCodeMetadata struct {
	Description string        `json:"description"`
	Format      string        `json:"format"`
	Context     int           `json:"context"`
	Modalities  []string      `json:"modalities"`
	Efforts     []string      `json:"efforts,omitempty"`
	Cost        openCodePrice `json:"cost"`
}

type openCodePrice struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	// Preserve tier data rather than silently treating it as a flat price.
	Tiers           jsontext.Value `json:"tiers,omitempty"`
	ContextOver200K *openCodePrice `json:"context_over_200k,omitempty"`
}

type openCodeProvider struct {
	NPM    string                      `json:"npm"`
	Models map[string]openCodeRawModel `json:"models"`
}

type openCodeRawModel struct {
	ReasoningOptions []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
	} `json:"reasoning_options"`
	Description string `json:"description"`
	Provider    struct {
		NPM string `json:"npm"`
	} `json:"provider"`
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Cost openCodePrice `json:"cost"`
}

//go:generate go run ./cmd/update-opencode-snapshot -output opencode_snapshot

// These are provider-owned source records, not a hand-maintained model table.
// Refresh them with the go:generate command above.
//
//go:embed opencode_snapshot/*.json
var openCodeSnapshotFiles embed.FS

var openCodeBootstrap = mustOpenCodeBootstrap()

func mustOpenCodeBootstrap() *openCodeSnapshot {
	metadata, err := openCodeSnapshotFiles.ReadFile("opencode_snapshot/metadata.json")
	if err != nil {
		panic(err)
	}
	models := make(map[string][]byte, 2)
	for _, prefix := range []string{"opencode-go", "opencode-zen"} {
		models[prefix], err = openCodeSnapshotFiles.ReadFile("opencode_snapshot/" + prefix + "-models.json")
		if err != nil {
			panic(err)
		}
	}
	snapshot, err := decodeOpenCodeSnapshot(metadata, models, nil, time.Time{})
	if err != nil {
		panic(err)
	}
	return snapshot
}

type openCodeCatalog struct {
	refreshGate chan struct{}
	snapshot    atomic.Pointer[openCodeSnapshot]
	nextAttempt time.Time
	path        string
	client      *http.Client
	metadataURL string
	modelURLs   map[string]string
}

func newOpenCodeCatalog() *openCodeCatalog {
	c := &openCodeCatalog{
		refreshGate: make(chan struct{}, 1),
		client:      &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		metadataURL: "https://models.opencode.ai/api.json",
		modelURLs: map[string]string{
			"opencode-go":  "https://opencode.ai/zen/go/v1/models",
			"opencode-zen": "https://opencode.ai/zen/v1/models",
		},
	}
	c.snapshot.Store(openCodeBootstrap)
	if directory, err := os.UserCacheDir(); err == nil {
		c.path = filepath.Join(directory, "mekugi", "opencode-models-v2.json")
		if data, err := readOpenCodeCache(c.path); err == nil {
			var snapshot openCodeSnapshot
			if json.Unmarshal(data, &snapshot) == nil && validOpenCodeSnapshot(&snapshot) {
				c.snapshot.Store(&snapshot)
			}
		}
	}
	return c
}

func readOpenCodeCache(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return boundedOpenCodeRead(file)
}

func boundedOpenCodeRead(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, openCodeCatalogLimit+1))
	if err == nil && len(data) > openCodeCatalogLimit {
		err = errors.New("OpenCode catalog exceeds size limit")
	}
	return data, err
}

func validOpenCodeSnapshot(s *openCodeSnapshot) bool {
	return !s.Updated.IsZero() && !s.Updated.After(time.Now().Add(time.Minute)) && validOpenCodeSnapshotData(s)
}

func validOpenCodeSnapshotData(s *openCodeSnapshot) bool {
	if s.Version != 2 {
		return false
	}
	for _, prefix := range []string{"opencode-go", "opencode-zen"} {
		if len(s.Models[prefix]) == 0 {
			return false
		}
		for id, m := range s.Models[prefix] {
			if !validOpenCodeID(id) || m.Context < 0 {
				return false
			}
			switch m.Format {
			case "", "chat", "anthropic", "responses":
			default:
				return false
			}
		}
	}
	return true
}

func validOpenCodeID(id string) bool {
	if id == "" || len(id) > 200 {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("-_./", char)) {
			return false
		}
	}
	return true
}

func (c *openCodeCatalog) current() *openCodeSnapshot {
	return c.snapshot.Load()
}

// Fresh launches/requests do no network I/O. Expired metadata refreshes before
// selection, with a bounded deadline and a short failure backoff. Inference is
// never retried, and requests already in flight retain their pinned snapshot.
func (c *openCodeCatalog) refresh(ctx context.Context, force bool) error {
	select {
	case c.refreshGate <- struct{}{}:
		defer func() { <-c.refreshGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now()
	if !force && (now.Sub(c.current().Updated) < openCodeCatalogTTL || now.Before(c.nextAttempt)) {
		return nil
	}
	c.nextAttempt = now.Add(time.Minute)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	metadata, err := c.get(ctx, c.metadataURL)
	if err != nil {
		return err
	}
	models := make(map[string][]byte, len(c.modelURLs))
	for prefix, url := range c.modelURLs {
		models[prefix], err = c.get(ctx, url)
		if err != nil {
			return err
		}
	}
	next, err := decodeOpenCodeSnapshot(metadata, models, c.current(), now)
	if err != nil {
		return err
	}
	c.snapshot.Store(next)
	if c.path == "" {
		return nil
	}
	return c.save(next)
}

func decodeOpenCodeSnapshot(metadata []byte, modelLists map[string][]byte, previous *openCodeSnapshot, updated time.Time) (*openCodeSnapshot, error) {
	var providers map[string]openCodeProvider
	if json.Unmarshal(metadata, &providers) != nil {
		return nil, errors.New("invalid OpenCode metadata")
	}
	next := &openCodeSnapshot{Version: 2, Updated: updated, Models: make(map[string]map[string]openCodeMetadata)}
	for _, prefix := range []string{"opencode-go", "opencode-zen"} {
		providerID := prefix
		if prefix == "opencode-zen" {
			providerID = "opencode"
		}
		provider, ok := providers[providerID]
		if !ok || len(provider.Models) == 0 {
			return nil, errors.New("OpenCode metadata is missing provider models")
		}
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(modelLists[prefix], &list) != nil || len(list.Data) == 0 {
			return nil, errors.New("invalid OpenCode model list")
		}
		next.Models[prefix] = make(map[string]openCodeMetadata, len(list.Data))
		for _, item := range list.Data {
			if !validOpenCodeID(item.ID) {
				return nil, errors.New("invalid OpenCode model ID")
			}
			raw, exists := provider.Models[item.ID]
			if !exists {
				// Live legacy aliases can outlast models.dev records. Retain
				// their last known metadata, but never guess a new API format.
				if previous != nil {
					next.Models[prefix][item.ID] = previous.Models[prefix][item.ID]
				} else {
					next.Models[prefix][item.ID] = openCodeMetadata{}
				}
				continue
			}
			npm := raw.Provider.NPM
			if npm == "" {
				npm = provider.NPM
			}
			format := ""
			switch npm {
			case "@ai-sdk/openai-compatible":
				format = "chat"
			case "@ai-sdk/anthropic":
				format = "anthropic"
			case "@ai-sdk/openai":
				format = "responses"
			}
			modalities := []string{"text"}
			for _, modality := range raw.Modalities.Input {
				if modality == "image" {
					modalities = append(modalities, modality)
					break
				}
			}
			var efforts []string
			for _, option := range raw.ReasoningOptions {
				if option.Type == "effort" {
					for _, value := range option.Values {
						if validOpenCodeID(value) && !slices.Contains(efforts, value) {
							efforts = append(efforts, value)
						}
					}
				}
			}
			next.Models[prefix][item.ID] = openCodeMetadata{
				Description: raw.Description,
				Format:      format, Context: raw.Limit.Context, Modalities: modalities, Cost: raw.Cost, Efforts: efforts,
			}
		}
	}
	if !validOpenCodeSnapshotData(next) {
		return nil, errors.New("invalid OpenCode catalog snapshot")
	}
	return next, nil
}

func (c *openCodeCatalog) get(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("invalid OpenCode catalog URL")
	}
	// Public catalogs never receive either OpenCode or Codex credentials.
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("OpenCode catalog fetch failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("OpenCode catalog fetch returned a non-success status")
	}
	return boundedOpenCodeRead(response.Body)
}

func (c *openCodeCatalog) save(snapshot *openCodeSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(c.path), ".opencode-models-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	err = errors.Join(writeErr, file.Close())
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), c.path)
}

// Explicit context tiers supersede the legacy 200k field. Unknown tier types
// cannot be priced safely. A missing rate is not a free rate.
func (price openCodePrice) forInput(input uint64) (openCodePrice, bool) {
	if len(price.Tiers) == 0 || string(price.Tiers) == "null" {
		if input > 200000 && price.ContextOver200K != nil {
			return *price.ContextOver200K, true
		}
		return price, true
	}
	var tiers []jsontext.Value
	if json.Unmarshal(price.Tiers, &tiers) != nil {
		return openCodePrice{}, false
	}
	selected := uint64(0)
	result := price
	for _, raw := range tiers {
		var tier struct {
			Tier struct {
				Type string `json:"type"`
				Size uint64 `json:"size"`
			} `json:"tier"`
		}
		if json.Unmarshal(raw, &tier) != nil || tier.Tier.Type != "context" || tier.Tier.Size == 0 {
			return openCodePrice{}, false
		}
		if input > tier.Tier.Size && tier.Tier.Size > selected {
			var next openCodePrice
			if json.Unmarshal(raw, &next) != nil {
				return openCodePrice{}, false
			}
			result, selected = next, tier.Tier.Size
		}
	}
	return result, true
}

func (price openCodePrice) estimate(tier string, counts tokenCounts) tokenCost {
	if (tier != "" && tier != "default") || counts.Incomplete || counts.Inconsistent ||
		counts.UncachedInputTokens > counts.InputTokens || counts.CacheWriteTokens > counts.UncachedInputTokens ||
		counts.ReasoningTokens > counts.OutputTokens {
		return tokenCost{}
	}
	var priceOK bool
	price, priceOK = price.forInput(counts.InputTokens)
	if !priceOK {
		return tokenCost{}
	}
	valid := func(value *float64) bool {
		return value != nil && *value >= 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0)
	}
	if !valid(price.Input) || !valid(price.Output) {
		return tokenCost{}
	}
	result := tokenCost{
		uncachedInput: float64(counts.UncachedInputTokens-counts.CacheWriteTokens) * *price.Input / 1e6,
		output:        float64(counts.OutputTokens) * *price.Output / 1e6,
		known:         true,
	}
	if cached := counts.InputTokens - counts.UncachedInputTokens; cached != 0 {
		if !valid(price.CacheRead) {
			return tokenCost{}
		}
		result.cachedInput = float64(cached) * *price.CacheRead / 1e6
	}
	if counts.CacheWriteTokens != 0 {
		if !valid(price.CacheWrite) {
			return tokenCost{}
		}
		result.uncachedInput += float64(counts.CacheWriteTokens) * *price.CacheWrite / 1e6
	}
	return result
}
