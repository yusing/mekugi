package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strconv"

	"github.com/yusing/mekugi/modules/conversation/presets"
)

// Presets borrows the host's effective configuration and active user-layer writer.
func (r *Requests[T]) Presets(ctx context.Context, thread, workspace string) presets.Store {
	return &presetStore[T]{requests: r, ctx: ctx, thread: thread, workspace: workspace}
}

type presetStore[T any] struct {
	requests          *Requests[T]
	ctx               context.Context
	thread, workspace string
}

type presetConfig struct {
	TUI struct {
		Presets map[string]presets.Slot `json:"presets"`
	} `json:"tui"`
}

type presetConfiguration struct {
	Config presetConfig `json:"config"`
	Layers []struct {
		Name struct {
			Type string `json:"type"`
			File string `json:"file"`
		} `json:"name"`
		Version        string                    `json:"version"`
		DisabledReason string                    `json:"disabledReason"`
		Config         map[string]jsontext.Value `json:"config"`
	} `json:"layers"`
}

func (s *presetStore[T]) read() (presetConfiguration, error) {
	raw, err := s.requests.Configuration(s.ctx, s.thread, s.workspace)
	var config presetConfiguration
	if err == nil {
		err = json.Unmarshal(raw, &config)
	}
	return config, err
}

func (s *presetStore[T]) Load() ([10]presets.Slot, error) {
	var slots [10]presets.Slot
	config, err := s.read()
	if err != nil {
		return slots, err
	}
	for slot := range slots {
		value, exists := config.Config.TUI.Presets[strconv.Itoa(slot)]
		if exists {
			if err := presets.Validate(value.Input); err != nil {
				return slots, fmt.Errorf("Read preset %d: %w", slot, err)
			}
			slots[slot] = value
		}
	}
	return slots, nil
}

func (s *presetStore[T]) Save(slot int, value presets.Slot) (presets.Slot, error) {
	config, err := s.read()
	if err != nil {
		return presets.Slot{}, err
	}
	// config/read supplies layers highest first, including disabled layers.
	// The first enabled user layer is the selected profile, or the base config.
	var file, version string
	for _, layer := range config.Layers {
		if layer.DisabledReason == "" && layer.Name.Type == "user" {
			file, version = layer.Name.File, layer.Version
			break
		}
	}
	if file == "" || version == "" {
		return presets.Slot{}, errors.New("Codex did not provide an active user configuration layer")
	}
	key := strconv.Itoa(slot)
	// Source: codex-rs/tui/src/app/quick_dials.rs:14:73@[0df4aa677] save_quick_dial.
	// Replacing cannot override a protected slot; deleting cannot reveal one.
	for _, layer := range config.Layers {
		if layer.DisabledReason != "" {
			continue
		}
		// Raw layers can contain partial or overridden values. Ownership needs
		// only slot presence, not a second decoding of their input and label.
		var tui struct {
			Presets map[string]jsontext.Value `json:"presets"`
		}
		if json.Unmarshal(layer.Config["tui"], &tui) != nil {
			continue
		}
		if _, exists := tui.Presets[key]; !exists {
			continue
		}
		if layer.Name.Type != "user" || layer.Name.File != file {
			return presets.Slot{}, fmt.Errorf("Preset %d is defined outside your active user config. Edit its source configuration instead", slot)
		}
		if value.Input != "" {
			break
		}
	}
	var write any
	if value.Input != "" {
		write = value // An explicit empty label masks any inherited label.
	} else {
		value = presets.Slot{}
	}
	_, err = s.requests.call(s.ctx, s.thread, "config/value/write", map[string]any{
		"keyPath": "tui.presets." + key, "value": write, "mergeStrategy": "replace",
		"filePath": file, "expectedVersion": version,
	}, nil)
	return value, err
}
