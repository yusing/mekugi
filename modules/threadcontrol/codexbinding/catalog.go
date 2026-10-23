package codexbinding

import (
	json "encoding/json/v2"
	"errors"
)

// CaptureCatalog receives the actual selected catalog before host projection.
// Malformed acquisition stays explicit to preparation without blocking unrelated
// model discovery or falling back to a different catalog.
func (d *Driver) CaptureCatalog(raw []byte) {
	var catalog struct {
		Models []struct {
			Name   string  `json:"slug"`
			Effort *string `json:"default_reasoning_level"`
			Levels []struct {
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
			model := Model{Name: entry.Name, DefaultEffort: entry.Effort, Root: entry.Messages.Agents.Role.Root, Child: entry.Messages.Agents.Role.Child}
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
