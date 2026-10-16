// Source: apps/mekugi/internal/router/app_server_picker.go:263:291 and app_server_skills.go:22:68@[0a356401] completion filtering.
package completion

import (
	"slices"
	"strings"
)

func MatchScore(name, query string) (int, bool) {
	lower := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "İ", "i\u0307")) }
	needle := []rune(lower(query))
	if len(needle) == 0 {
		return 0, true
	}
	first, last, matched := -1, 0, 0
	for i, r := range []rune(lower(name)) {
		if r != needle[matched] {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
		matched++
		if matched == len(needle) {
			break
		}
	}
	if matched != len(needle) {
		return 0, false
	}
	score := last - first + 1 - len(needle)
	if first == 0 {
		score -= 100
	}
	return score, true
}

func (p *State) FilterSkills(query string, managing bool) {
	selected := ""
	if p.Selected < len(p.Choices) {
		selected = p.Choices[p.Selected].Path
	}
	p.Choices = nil
	for _, skill := range p.Skills {
		if managing && skill.Path == "" {
			continue
		}
		if !skill.Enabled && !managing {
			continue
		}
		_, displayMatch := MatchScore(skill.Label(), query)
		_, nameMatch := MatchScore(skill.Name, query)
		if displayMatch || nameMatch {
			p.Choices = append(p.Choices, skill)
		}
	}
	if query != "" || !managing {
		slices.SortStableFunc(p.Choices, func(a, b Choice) int {
			ax, am := MatchScore(a.Label(), query)
			bx, bm := MatchScore(b.Label(), query)
			if !am {
				ax, _ = MatchScore(a.Name, query)
			}
			if !bm {
				bx, _ = MatchScore(b.Name, query)
			}
			if !managing && am != bm {
				if am {
					return -1
				}
				return 1
			}
			if ax != bx {
				return ax - bx
			}
			return strings.Compare(a.Label(), b.Label())
		})
	}
	if managing {
		p.Selected = max(0, slices.IndexFunc(p.Choices, func(c Choice) bool { return c.Path == selected }))
	}
	p.Selected = min(p.Selected, max(0, len(p.Choices)-1))
}
