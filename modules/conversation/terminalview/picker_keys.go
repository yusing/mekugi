// Source: apps/mekugi/internal/router/app_server_picker.go:414:460 and app_server_skills.go:99:162@[0a356401] completion key interpretation.
package terminalview

import (
	"unicode/utf8"

	"github.com/yusing/mekugi/modules/conversation/completion"
)

type PickerAction uint8

const (
	PickerPass PickerAction = iota
	PickerHandled
	PickerDismiss
	PickerAccept
	PickerCloseSkills
	PickerToggleSkill
	PickerChooseSkills
)

func PickerKey(p *completion.State, key string) PickerAction {
	if !p.Open {
		return PickerPass
	}
	switch key {
	case "\x1b":
		p.Dismissed, p.Open = p.Target, false
		return PickerDismiss
	case "\x1b[A", "\x1bOA", "\x10", "\x1b[B", "\x1bOB", "\x0e":
		if n := len(p.Choices); n > 0 {
			step := 1
			if key == "\x1b[A" || key == "\x1bOA" || key == "\x10" {
				step = -1
			}
			p.Selected = (p.Selected + step + n) % n
		}
		return PickerHandled
	case "\t", "\r":
		if len(p.Choices) > 0 {
			return PickerAccept
		}
		p.Dismissed, p.Open = p.Target, false
		if key == "\t" || p.Target.Kind == '$' {
			return PickerHandled
		}
	}
	return PickerPass
}

func SkillsKey(p *completion.State, menu bool, key string) PickerAction {
	if key == "\x1b[200~" || key == "\x1b[201~" {
		return PickerPass
	}
	n := len(p.Choices)
	if menu {
		n = 2
	}
	switch key {
	case "\x1b", "\x03":
		return PickerCloseSkills
	case "\x1b[A", "\x1bOA", "\x10":
		if n > 0 {
			p.Selected = (p.Selected + n - 1) % n
		}
	case "\x1b[B", "\x1bOB", "\x0e":
		if n > 0 {
			p.Selected = (p.Selected + 1) % n
		}
	case "\x1b[5~":
		p.Selected = max(0, p.Selected-8)
	case "\x1b[6~":
		p.Selected = min(max(0, n-1), p.Selected+8)
	case "\x1b[H", "\x1bOH":
		p.Selected = 0
	case "\x1b[F", "\x1bOF":
		p.Selected = max(0, n-1)
	case "\r", " ":
		if !menu {
			return PickerToggleSkill
		}
		if key == " " {
			break
		}
		return PickerChooseSkills
	case "1", "2":
		if menu {
			p.Selected = int(key[0] - '1')
			return PickerChooseSkills
		} else {
			p.Query += key
		}
	case "\x7f", "\x08":
		if !menu && p.Query != "" {
			_, n := utf8.DecodeLastRuneInString(p.Query)
			p.Query = p.Query[:len(p.Query)-n]
		}
	default:
		if !menu && len(key) == 1 && key[0] >= 32 {
			p.Query += key
		}
	}
	return PickerHandled
}
