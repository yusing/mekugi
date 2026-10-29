package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (d *Driver) listModels(ctx context.Context, caller control.Identity, raw jsontext.Value) (any, error) {
	var args struct{}
	if err := json.Unmarshal(raw, &args, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	member, err := d.control.Caller(caller)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(member.Settings.Model, "claude:") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d.mu.RLock()
		defer d.mu.RUnlock()
		if d.catalogError != nil {
			return nil, d.catalogError
		}
		if len(d.models) == 0 {
			return nil, errors.New("selected Codex model catalog is unavailable")
		}
		models := make([]control.ModelOption, 0, len(d.models))
		for _, model := range d.models {
			models = append(models, control.ModelOption{ID: model.Name})
		}
		return struct {
			Models []control.ModelOption `json:"models"`
		}{models}, nil
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

// CaptureConfiguredCatalog receives the host's effective config. Static
// catalogs bypass model discovery, so capture their selected source directly.
func (d *Driver) CaptureConfiguredCatalog(raw []byte) {
	var response struct {
		Config struct {
			Path string `json:"model_catalog_json"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Config.Path == "" {
		return
	}
	catalog, err := os.ReadFile(response.Config.Path)
	if err != nil {
		d.mu.Lock()
		d.models, d.catalogError = nil, err
		d.mu.Unlock()
		return
	}
	d.CaptureCatalog(catalog)
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

// InheritedEffort applies the same catalog policy to internal side forks.
func (d *Driver) InheritedEffort(model, effort string) (string, error) {
	if effort != "ultra" {
		return effort, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.catalogError != nil {
		return "", d.catalogError
	}
	return inheritedEffortForModel(d.models, model, effort)
}
