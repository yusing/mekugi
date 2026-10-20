package opencode

import (
	"maps"
	"slices"
)

// Source: internal/router/opencode.go:19:94@[18475aaf] service snapshots.
type Service struct {
	Prefix, Label, Endpoint, APIKey string
	Catalog                         *Catalog
	snapshot                        *openCodeSnapshot
}

func (c Config) Services() []Service {
	var services []Service
	if c.Go.APIKey != "" {
		services = append(services, Service{
			Prefix: "opencode-go", Label: "OpenCode Go", Endpoint: "https://opencode.ai/zen/go/v1/chat/completions", APIKey: c.Go.APIKey, Catalog: c.Catalog,
		})
	}
	if c.Zen.APIKey != "" {
		services = append(services, Service{
			Prefix: "opencode-zen", Label: "OpenCode Zen", Endpoint: "https://opencode.ai/zen/v1/chat/completions", APIKey: c.Zen.APIKey, Catalog: c.Catalog,
		})
	}
	return services
}

type Model struct {
	ID string
	Metadata
}

// The gateway enforces each model's endpoint format; it does not translate
// arbitrary models through the Chat Completions endpoint.
// Pin keeps format and metadata stable throughout an individual request.
func (s Service) Pin() Service {
	if s.snapshot != nil {
		return s
	}
	if s.Catalog != nil {
		s.snapshot = s.Catalog.current()
	} else {
		s.snapshot = openCodeBootstrap
	}
	return s
}

func (s Service) Format(id string) string {
	return s.Pin().snapshot.Models[s.Prefix][id].Format
}

func (s Service) Models() []Model {
	snapshot := s.Pin().snapshot
	var models []Model
	for _, id := range slices.Sorted(maps.Keys(snapshot.Models[s.Prefix])) {
		entry := snapshot.Models[s.Prefix][id]
		if entry.Format != "" {
			models = append(models, Model{ID: id, Metadata: entry})
		}
	}
	return models
}

// DefaultModel picks a stable available model from the first authenticated
// service. An explicit invocation model always takes precedence.
func (c Config) DefaultModel() string {
	for _, service := range c.Services() {
		if models := service.Models(); len(models) > 0 {
			return service.Prefix + ":" + models[0].ID
		}
	}
	return ""
}

func (s Service) Model(id string) (Metadata, bool) {
	entry, ok := s.Pin().snapshot.Models[s.Prefix][id]
	return entry, ok
}
