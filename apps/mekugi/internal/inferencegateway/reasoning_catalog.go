package inferencegateway

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
)

// ReasoningCatalog borrows the selected provider's unprojected model catalog.
// It supplies auxiliary request policy without owning agent admission/settings.
type ReasoningCatalog struct {
	mu           sync.RWMutex
	models       []reasoningModel
	err          error
	NativeModels bool
}

type reasoningModel struct {
	Name       string  `json:"slug"`
	Default    string  `json:"default_reasoning_level"`
	MultiAgent *string `json:"multi_agent_reasoning_effort"`
	Updates    bool    `json:"supports_reasoning_effort_updates"`
	Levels     []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

func (c *ReasoningCatalog) Capture(raw []byte) {
	var catalog struct {
		Models []reasoningModel `json:"models"`
	}
	err := json.Unmarshal(raw, &catalog)
	if err == nil && len(catalog.Models) == 0 {
		err = errors.New("selected model catalog contains no models")
	}
	if err != nil {
		catalog.Models = nil
	}
	c.mu.Lock()
	c.models, c.err = catalog.Models, err
	c.mu.Unlock()
}

func (c *ReasoningCatalog) Choices(name string) (string, []string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, model := range c.models {
		if model.Name == name {
			levels := make([]string, len(model.Levels))
			for i, level := range model.Levels {
				levels[i] = level.Effort
			}
			return model.Default, levels, model.Updates
		}
	}
	return "", nil, false
}

// CaptureConfigured reads an explicitly configured catalog that bypasses /models.
func (c *ReasoningCatalog) CaptureConfigured(raw []byte) {
	var response struct {
		Config struct {
			Path string `json:"model_catalog_json"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Config.Path == "" {
		return
	}
	raw, err := os.ReadFile(response.Config.Path)
	if err == nil {
		c.Capture(raw)
		return
	}
	c.mu.Lock()
	c.models, c.err = nil, err
	c.mu.Unlock()
}

// InheritedEffort prevents a Side fork from inheriting proactive ultra mode.
// Configure NativeModels before requests when the selected custom provider owns it.
func (c *ReasoningCatalog) InheritedEffort(model, effort string) (string, error) {
	if effort != "ultra" || c.NativeModels {
		return effort, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.err != nil {
		return "", c.err
	}
	for _, candidate := range c.models {
		if candidate.Name != model {
			continue
		}
		levels := make([]string, len(candidate.Levels))
		for i, level := range candidate.Levels {
			levels[i] = level.Effort
		}
		if candidate.MultiAgent != nil && *candidate.MultiAgent != "ultra" && slices.Contains(levels, *candidate.MultiAgent) {
			return *candidate.MultiAgent, nil
		}
		if slices.Contains(levels, "max") {
			return "max", nil
		}
		for _, level := range slices.Backward(levels) {
			if level != "ultra" {
				return level, nil
			}
		}
		return "medium", nil
	}
	return "", fmt.Errorf("inherited ultra requires the selected model catalog for %s", model)
}
