// Source: apps/mekugi/internal/router/app_server_picker.go:79:140 and app_server_skills.go:222:254@[0a356401] completion targets.
package completion

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (p *State) FindTarget(d composer.Draft) Target {
	at := d.Cursor()
	if strings.HasPrefix(d.Text, "/") && at > 0 {
		end := strings.IndexFunc(d.Text, unicode.IsSpace)
		if end < 0 {
			end = len(d.Text)
		}
		if at <= end {
			return Target{Kind: '/', End: end, Query: d.Text[1:end]}
		}
	}
	start := at
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(d.Text[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= n
	}
	if start == len(d.Text) || (d.Text[start] != '@' && d.Text[start] != '$') {
		return Target{}
	}
	end := at
	for end < len(d.Text) {
		r, n := utf8.DecodeRuneInString(d.Text[end:])
		if unicode.IsSpace(r) {
			break
		}
		end += n
	}
	if at < start {
		return Target{}
	}
	for _, span := range d.Spans() {
		if start < span.End && end > span.Start {
			return Target{}
		}
	}
	query := d.Text[start+1 : end]
	if d.Text[start] == '$' && !p.SkillQueryCompletable(query) {
		return Target{}
	}
	return Target{d.Text[start], start, end, query}
}

func (p *State) SkillQueryCompletable(query string) bool {
	if query == "" {
		return true
	}
	end := 0
	for _, r := range query {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ':') {
			break
		}
		end++
	}
	if end == 0 {
		return false
	}
	name := query[:end]
	if slices.Contains([]string{"PATH", "HOME", "USER", "SHELL", "PWD", "TMPDIR", "TEMP", "TMP", "LANG", "TERM", "XDG_CONFIG_HOME"}, name) {
		return false
	}
	if name == "-" || name == "_" || strings.Trim(name, "0123456789") == "" {
		return false
	}
	if name[0] == '-' || name[0] >= '0' && name[0] <= '9' {
		for _, skill := range p.Skills {
			if skill.Enabled {
				if _, ok := MatchScore(skill.Name, query); ok {
					return true
				}
			}
		}
		return false
	}
	return true
}
