package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"slices"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (d *Driver) listModels(ctx context.Context, caller control.Identity, raw jsontext.Value) (any, error) {
	var args struct{}
	if err := json.Unmarshal(raw, &args, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if _, err := d.control.Caller(caller); err != nil {
		return nil, err
	}
	discovery, ok := d.native().(control.ModelDiscovery)
	if !ok {
		return nil, errors.New("Claude model discovery is unavailable")
	}
	models, err := discovery.DiscoverModels(ctx, caller)
	if err != nil {
		return nil, err
	}
	return struct {
		Models []control.ModelOption `json:"models"`
	}{models}, nil
}

// InheritCatalog freezes the selected provider catalog for another admitted
// runtime. The published model values are immutable after capture.
func (d *Driver) InheritCatalog(parent *Driver) {
	parent.mu.RLock()
	models, err := slices.Clone(parent.models), parent.catalogError
	parent.mu.RUnlock()
	d.mu.Lock()
	d.models, d.catalogError = models, err
	d.mu.Unlock()
}

// CaptureCatalog receives the actual selected catalog before host projection.
// Malformed acquisition stays explicit to preparation without blocking unrelated
// model discovery or falling back to a different catalog.
func (d *Driver) CaptureCatalog(raw []byte) {
	var catalog struct {
		Models []struct {
			Name             string  `json:"slug"`
			Effort           *string `json:"default_reasoning_level"`
			MultiAgentEffort *string `json:"multi_agent_reasoning_effort"`
			Levels           []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Tiers []struct {
				ID string `json:"id"`
			} `json:"service_tiers"`
			Messages struct {
				Agents struct {
					Role struct {
						Root  *string `json:"root"`
						Child *string `json:"subagent"`
					} `json:"role"`
				} `json:"multi_agent"`
			} `json:"model_messages"`
		} `json:"models"`
	}
	err := json.Unmarshal(raw, &catalog)
	if err == nil && len(catalog.Models) == 0 {
		err = errors.New("selected model catalog contains no models")
	}
	var models []Model
	if err == nil {
		for _, entry := range catalog.Models {
			model := Model{Name: entry.Name, DefaultEffort: entry.Effort, MultiAgentEffort: entry.MultiAgentEffort, Root: entry.Messages.Agents.Role.Root, Child: entry.Messages.Agents.Role.Child}
			for _, level := range entry.Levels {
				model.Efforts = append(model.Efforts, level.Effort)
			}
			for _, tier := range entry.Tiers {
				model.ServiceTiers = append(model.ServiceTiers, tier.ID)
			}
			models = append(models, model)
		}
	}
	d.mu.Lock()
	d.models, d.catalogError = models, err
	d.mu.Unlock()
}

// ReasoningChoices borrows the selected catalog for auxiliary request policy.
func (d *Driver) ReasoningChoices(name string) (string, []string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, model := range d.models {
		if model.Name == name {
			var fallback string
			if model.DefaultEffort != nil {
				fallback = *model.DefaultEffort
			}
			return fallback, slices.Clone(model.Efforts)
		}
	}
	return "", nil
}
