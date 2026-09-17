package router

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
)

func openCodeSessionID(provider, thread string) string {
	if strings.TrimSpace(thread) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(provider + "\x00" + strings.TrimSpace(thread)))
	return "mekugi-" + hex.EncodeToString(sum[:])
}

type openCodeService struct {
	prefix, label, endpoint, apiKey string
	catalog                         *openCodeCatalog
	snapshot                        *openCodeSnapshot
}

func (c OpenCodeConfig) services() []openCodeService {
	var services []openCodeService
	if c.Go.APIKey != "" {
		services = append(services, openCodeService{
			prefix: "opencode-go", label: "OpenCode Go", endpoint: "https://opencode.ai/zen/go/v1/chat/completions", apiKey: c.Go.APIKey, catalog: c.catalog,
		})
	}
	if c.Zen.APIKey != "" {
		services = append(services, openCodeService{
			prefix: "opencode-zen", label: "OpenCode Zen", endpoint: "https://opencode.ai/zen/v1/chat/completions", apiKey: c.Zen.APIKey, catalog: c.catalog,
		})
	}
	return services
}

type openCodeModel struct {
	id string
	openCodeMetadata
}

// The gateway enforces each model's endpoint format; it does not translate
// arbitrary models through the Chat Completions endpoint.
// pin keeps format and metadata stable throughout an individual request.
func (s openCodeService) pin() openCodeService {
	if s.snapshot != nil {
		return s
	}
	if s.catalog != nil {
		s.snapshot = s.catalog.current()
	} else {
		s.snapshot = openCodeBootstrap
	}
	return s
}

func (s openCodeService) format(id string) string {
	return s.pin().snapshot.Models[s.prefix][id].Format
}

func (s openCodeService) models() []openCodeModel {
	snapshot := s.pin().snapshot
	var models []openCodeModel
	for _, id := range slices.Sorted(maps.Keys(snapshot.Models[s.prefix])) {
		entry := snapshot.Models[s.prefix][id]
		if entry.Format != "" {
			models = append(models, openCodeModel{id: id, openCodeMetadata: entry})
		}
	}
	return models
}

func (s openCodeService) model(id string) (openCodeMetadata, bool) {
	entry, ok := s.pin().snapshot.Models[s.prefix][id]
	return entry, ok
}

func (s openCodeService) efforts(id string) []string {
	return s.pin().snapshot.Models[s.prefix][id].Efforts
}

func (m openCodeMetadata) reasoningLevels() []map[string]string {
	levels := []map[string]string{}
	for _, effort := range m.Efforts {
		levels = append(levels, map[string]string{"effort": effort, "description": effort + " reasoning"})
	}
	return levels
}

func isOpenCodeModel(model string) bool {
	return strings.HasPrefix(model, "opencode-go:") || strings.HasPrefix(model, "opencode-zen:")
}

func isChatCompletionsModel(model string) bool {
	return isGrokModel(model) || isOpenCodeModel(model)
}
