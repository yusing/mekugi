package inferencegateway

import (
	"maps"
	"sync"
)

// Source: apps/mekugi/internal/router/service_tier.go:5:41@[856efe05]

type Tiers struct {
	configured map[string]string
	choices    sync.Map // [thread, requested model] -> confirmed tier
}

func (s *Tiers) Choice(model, thread string) (string, bool) {
	if s != nil {
		if tier, ok := s.choices.Load([2]string{thread, model}); ok {
			return tier.(string), true
		}
	}
	return "", false
}

// Effective follows exact request model selection and leaves host settings untouched.
func (settings *Tiers) Effective(model, requested, thread string) string {
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
	s.choices.Store([2]string{thread, model}, tier)
}
