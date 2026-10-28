package presets

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/modules/conversation/completion"
)

type Stage string

const (
	Quick   Stage = "presets"
	Edit    Stage = "preset-edit"
	Label   Stage = "preset-label"
	Slots   Stage = "preset-slots"
	Confirm Stage = "preset-confirm"
)

// Flow is view-local. Storage results are polled by the application, never a renderer.
type Flow struct {
	Stage                                 Stage
	Slots                                 [10]Slot
	Input, Label, Seed, Settings, Problem string
	Slot                                  int
	Mode                                  string
	Pending, Loaded                       bool
	result                                chan result
	store                                 Store
}
type result struct {
	slots [10]Slot
	slot  Slot
	err   error
	saved bool
}

func (f *Flow) Open(store Store, seed, settings string) {
	*f = Flow{Stage: Quick, Slot: -1, Seed: seed, Settings: settings, store: store, Pending: true, result: make(chan result, 1)}
	ch := f.result
	go func() { slots, err := store.Load(); ch <- result{slots: slots, err: err} }()
}
func (f *Flow) Poll() bool {
	if !f.Pending {
		return false
	}
	select {
	case r := <-f.result:
		f.Pending = false
		if r.err != nil {
			f.Problem = r.err.Error()
			return true
		}
		f.Problem = ""
		if r.saved {
			f.Slots[f.Slot] = r.slot
			f.Stage = Quick
			f.Mode = ""
		} else {
			f.Slots = r.slots
			f.Loaded = true
		}
		return true
	default:
		return false
	}
}
func (f *Flow) Title() string {
	switch f.Stage {
	case Edit:
		if f.Slot >= 0 {
			return fmt.Sprintf("Edit preset %d", f.Slot)
		}
		return "New preset"
	case Label:
		switch {
		case f.Mode == "rename":
			return fmt.Sprintf("Rename preset %d", f.Slot)
		case f.Slot >= 0:
			return fmt.Sprintf("Label preset %d", f.Slot)
		}
		return "Label new preset"
	case Slots:
		switch f.Mode {
		case "edit":
			return "Edit a preset"
		case "rename":
			return "Rename a preset"
		case "delete":
			return "Delete a preset"
		}
		return "Save preset to slot"
	case Confirm:
		if f.Mode == "delete" {
			return fmt.Sprintf("Delete preset %d?", f.Slot)
		}
		return fmt.Sprintf("Replace preset %d?", f.Slot)
	}
	return "Presets"
}
func (f *Flow) Explanation() string {
	switch f.Stage {
	case Quick:
		return "Load a preset into the composer without sending."
	case Edit:
		return "Write a prompt or slash command."
	case Label:
		return "Name it in the menu, or leave empty to show its first line."
	case Confirm:
		if f.Mode == "delete" {
			return f.Slots[f.Slot].Input
		}
		return f.Slots[f.Slot].Input + "\n→ " + f.Input
	}
	return "Choose a numbered slot."
}
func (f *Flow) Choices() []completion.Choice {
	if f.Pending {
		return nil
	}
	if !f.Loaded {
		return []completion.Choice{{Name: "retry", Display: "Retry loading presets"}}
	}
	if f.Stage == Edit {
		return []completion.Choice{{Name: "continue", Display: "Continue"}}
	}
	if f.Stage == Label {
		display := "Continue"
		if f.Mode == "rename" || f.Slot >= 0 && f.Slots[f.Slot].Input == "" {
			display = "Save"
		}
		return []completion.Choice{{Name: "continue", Display: display}}
	}
	if f.Stage == Confirm {
		return []completion.Choice{{Name: "confirm", Display: strings.TrimSuffix(f.Title(), "?")}, {Name: "cancel", Display: "Cancel"}}
	}
	choices := []completion.Choice{}
	for i := range 10 {
		slot := (i + 1) % 10
		input := f.Slots[slot].Input
		if f.Stage == Slots && f.Mode != "save" && input == "" {
			continue
		}
		label := "empty"
		if input != "" {
			text := input
			if strings.TrimSpace(f.Slots[slot].Label) != "" {
				text = f.Slots[slot].Label // Codex shows a configured label's first line too.
			}
			label, _, _ = strings.Cut(strings.TrimSpace(text), "\n")
		}
		choices = append(choices, completion.Choice{ID: uint64(slot + 1), Name: fmt.Sprint(slot), Display: fmt.Sprintf("%d  %s", slot, label)})
	}
	if f.Stage == Quick {
		choices = append(choices, completion.Choice{Name: "n", Display: "New preset"}, completion.Choice{Name: "s", Display: "From current model settings"}, completion.Choice{Name: "e", Display: "Edit a preset"}, completion.Choice{Name: "r", Display: "Rename a preset"}, completion.Choice{Name: "d", Display: "Delete a preset"})
	}
	return choices
}

// NamedChoices exposes saved labels without turning their input into a binding.
func (f *Flow) NamedChoices() []completion.Choice {
	if !f.Loaded {
		return nil
	}
	var choices []completion.Choice
	for i := range 10 {
		slot := (i + 1) % 10
		value := f.Slots[slot]
		label, _, _ := strings.Cut(strings.TrimSpace(value.Label), "\n")
		if value.Input == "" || label == "" {
			continue
		}
		preview, _, _ := strings.Cut(strings.TrimSpace(value.Input), "\n")
		choices = append(choices, completion.Choice{ID: uint64(slot + 1), Name: label, Description: fmt.Sprintf("Preset %d: %s", slot, preview), Preset: true})
	}
	return choices
}

// Choose returns recalled input to fill the client's composer without submitting.
func (f *Flow) Choose(choice completion.Choice, input string) string {
	if f.Pending {
		return ""
	}
	f.Problem = ""
	if choice.Name == "retry" {
		f.Open(f.store, f.Seed, f.Settings)
		return ""
	}
	if choice.Name == "cancel" {
		f.Stage = Quick
		return ""
	}
	switch f.Stage {
	case Edit:
		f.Input = input
		if err := Validate(input); err != nil {
			f.Problem = err.Error()
			return ""
		}
		f.Stage = Label
		f.Label = ""
		if f.Slot >= 0 {
			f.Label = f.Slots[f.Slot].Label
		}
	case Label:
		f.Label = strings.TrimSpace(input)
		if err := ValidateLabel(f.Label); err != nil {
			f.Label = input
			f.Problem = err.Error()
			return ""
		}
		if f.Mode == "rename" {
			f.save()
		} else if f.Slot < 0 {
			f.Stage = Slots
			f.Mode = "save"
		} else if f.Slots[f.Slot].Input != "" {
			f.Stage = Confirm
		} else {
			f.save()
		}
	case Confirm:
		f.save()
	case Quick:
		switch choice.Name {
		case "n", "s":
			f.Stage = Edit
			f.Slot, f.Mode = -1, ""
			f.Input = f.Seed
			if choice.Name == "s" {
				f.Input = f.Settings
			}
			return ""
		case "e", "r", "d":
			f.Stage = Slots
			f.Mode = map[string]string{"e": "edit", "r": "rename", "d": "delete"}[choice.Name]
			return ""
		}
		if choice.ID < 1 || choice.ID > 10 {
			return ""
		}
		f.Slot = int(choice.ID) - 1
		if f.Slots[f.Slot].Input != "" {
			return f.Slots[f.Slot].Input
		}
		f.Input = f.Seed
		f.Stage = Edit
		f.Mode = "save"
	case Slots:
		if choice.ID < 1 || choice.ID > 10 {
			return ""
		}
		f.Slot = int(choice.ID) - 1
		switch f.Mode {
		case "edit":
			f.Input = f.Slots[f.Slot].Input
			f.Stage = Edit
		case "rename":
			f.Input, f.Label = f.Slots[f.Slot].Input, f.Slots[f.Slot].Label
			f.Stage = Label
		case "delete":
			f.Input, f.Label = "", ""
			f.Stage = Confirm
		case "save":
			if f.Slots[f.Slot].Input != "" {
				f.Stage = Confirm
			} else {
				f.save()
			}
		}
	}
	return ""
}

// Query is the text a client edits at the current stage.
func (f *Flow) Query() string {
	if f.Stage == Label {
		return f.Label
	}
	return f.Input
}
func (f *Flow) save() {
	f.Pending = true
	slot, value, store, ch := f.Slot, Slot{Input: f.Input, Label: f.Label}, f.store, f.result
	go func() { saved, err := store.Save(slot, value); ch <- result{saved: true, slot: saved, err: err} }()
}
