package inferencegateway

import (
	"maps"
	"sync"
)

// Source: apps/mekugi/internal/router/service_tier.go:5:41@[856efe05]

type Tiers struct {
	configured map[string]string
	choices    sync.Map // [thread, routed model] -> confirmed tier
}

// RoutedModel resolves the retained selectable model alias before provider policy.
func RoutedModel(model string) string {
	if model == "gpt-5.6-terra" {
		return "gpt-6-sol"
	}
	return model
}

func (s *Tiers) Choice(model, thread string) (string, bool) {
	if s != nil {
		if tier, ok := s.choices.Load([2]string{thread, RoutedModel(model)}); ok {
			return tier.(string), true
		}
	}
	return "", false
}

// Effective follows request model selection and leaves host settings
// untouched. The legacy Terra selection routes to Sol before tier lookup.
func (settings *Tiers) Effective(model, requested, thread string) string {
	model = RoutedModel(model)
	if settings != nil {
		if tier, ok := settings.Choice(model, thread); ok {
			requested = tier
		} else if tier := settings.configured[model]; tier != "" {
			requested = tier
		}
	}
	if requested == "fast" {
		return "priority"
	}
	return requested
}

// NewTiers snapshots invocation defaults independently of caller configuration.
func NewTiers(configured map[string]string) *Tiers {
	return &Tiers{configured: maps.Clone(configured)}
}

// Confirm admits an authoritative explicit choice without changing invocation defaults.
func (s *Tiers) Confirm(model, thread, tier string) {
	if tier == "" {
		tier = "default"
	}
	s.choices.Store([2]string{thread, RoutedModel(model)}, tier)
}
