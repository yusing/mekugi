package toolplugin

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/outputbudget"
)

type ReadRequest struct {
	Stdout     string
	Stderr     string
	StdoutKind string
	StderrKind string
	Position   [2]int
	Stream     string
	SourceRow  uint64
	Label      string
}
type ReadPage struct {
	Text         string
	Position     [2]int
	Complete     bool
	NeededTokens int
}

// Source: plugins/read_output.ts:44:134@[543de4f3] selectReadOutput
func Page(ctx context.Context, req ReadRequest, budget int) (ReadPage, error) {
	if err := ctx.Err(); err != nil {
		return ReadPage{}, err
	}
	b := new(outputbudget.Budget)
	values := [2]string{req.Stdout, req.Stderr}
	kinds := [2]string{req.StdoutKind, req.StderrKind}
	positions := req.Position
	pages := [2]string{}
	var entries [2][]jsontext.Value
	for i := range 2 {
		if kinds[i] == "json" {
			if err := json.Unmarshal([]byte(values[i]), &entries[i], jsontext.AllowInvalidUTF8(true)); err != nil {
				return ReadPage{}, err
			}
			if strings.TrimSpace(values[i]) == "null" {
				return ReadPage{}, errors.New("structured read output must be a JSON array")
			}
		}
	}
	included := func(i int) bool { return req.Stream == "" || req.Stream == [2]string{"stdout", "stderr"}[i] }
	size := func(i int) int {
		if kinds[i] == "json" {
			return len(entries[i])
		}
		return len(values[i])
	}
	for i := range 2 {
		if positions[i] < 0 || positions[i] > size(i) {
			return ReadPage{}, errors.New("invalid read position")
		}
	}
	sourceStart := req.SourceRow
	if sourceStart > 0 {
		sourceStart += uint64(strings.Count(req.Stdout[:req.Position[0]], "\n"))
	}
	frame := func() string {
		prefix := ""
		if req.Label != "" {
			prefix = "--- " + req.Label + " ---\n"
		}
		if pages[0] != "" && sourceStart > 0 {
			prefix += fmt.Sprintf("[rows %d:%d]\n", sourceStart, sourceStart+uint64(strings.Count(pages[0], "\n"))-1)
		}
		if pages[1] == "" {
			if pages[0] != "" {
				return prefix + pages[0]
			}
			return ""
		}
		for i, page := range pages {
			if page != "" {
				name := [2]string{"stdout", "stderr"}[i]
				kind := kinds[i]
				if kind == "" {
					kind = "bytes"
				}
				prefix += fmt.Sprintf("[%s %s]\n%s\n[/%s]\n", name, kind, page, name)
			}
		}
		return prefix
	}
	needed := func() (ReadPage, error) {
		n, e := b.Count(frame())
		return ReadPage{Position: req.Position, NeededTokens: n}, e
	}
	for i := range 2 {
		if included(i) && kinds[i] == "json" && len(entries[i]) == 0 {
			pages[i] = "[]"
		}
	}
	fit, err := b.Fits(frame(), budget)
	if err != nil {
		return ReadPage{}, err
	}
	if !fit {
		return needed()
	}
	advanced := false
	arrayPage := func(i, offset, count int) string {
		var v strings.Builder
		v.WriteByte('[')
		for j := 0; j < count; j++ {
			if j > 0 {
				v.WriteByte(',')
			}
			v.WriteString(strings.TrimSpace(string(entries[i][offset+j])))
		}
		v.WriteByte(']')
		return v.String()
	}
	for i := range 2 {
		if !included(i) || positions[i] == size(i) {
			continue
		}
		offset := positions[i]
		if kinds[i] == "json" {
			low, high, bytes := 0, 0, 2
			for offset+high < len(entries[i]) && bytes <= budget*128 {
				bytes += len(entries[i][offset+high]) + 1
				high++
			}
			for low < high {
				mid := (low + high + 1) / 2
				pages[i] = arrayPage(i, offset, mid)
				fit, e := b.Fits(frame(), budget)
				if e != nil {
					return ReadPage{}, e
				}
				if fit {
					low = mid
				} else {
					high = mid - 1
				}
			}
			pages[i] = ""
			if low > 0 {
				pages[i] = arrayPage(i, offset, low)
			}
			positions[i] += low
		} else {
			if offset < len(values[i]) && !utf8.RuneStart(values[i][offset]) {
				return ReadPage{}, errors.New("read position splits UTF-8")
			}
			if kinds[i] == "rows" && offset > 0 && values[i][offset-1] != '\n' {
				return ReadPage{}, errors.New("read position splits a complete row")
			}
			end := min(len(values[i]), offset+budget*128+4)
			for end < len(values[i]) && !utf8.RuneStart(values[i][end]) {
				end--
			}
			remaining := values[i][offset:end]
			allowance := budget
			for allowance > 0 {
				selected, e := b.SelectText(remaining, allowance, false)
				if e != nil {
					return ReadPage{}, e
				}
				pages[i] = selected.Text
				if kinds[i] == "rows" && (len(pages[i]) < len(remaining) || end < len(values[i])) {
					pages[i] = pages[i][:strings.LastIndexByte(pages[i], '\n')+1]
				}
				n, e := b.Count(frame())
				if e != nil {
					return ReadPage{}, e
				}
				excess := n - budget
				if excess <= 0 {
					break
				}
				allowance -= max(1, excess)
			}
			if allowance <= 0 {
				pages[i] = ""
			}
			positions[i] += len(pages[i])
		}
		if positions[i] == offset {
			if !advanced {
				if kinds[i] == "json" {
					pages[i] = arrayPage(i, offset, 1)
				} else {
					remaining := values[i][offset:]
					_, end := utf8.DecodeRuneInString(remaining)
					if kinds[i] == "rows" {
						end = strings.IndexByte(remaining, '\n') + 1
						if end == 0 {
							end = len(remaining)
						}
					}
					pages[i] = remaining[:end]
				}
				return needed()
			}
			break
		}
		advanced = true
		if positions[i] < size(i) {
			break
		}
	}
	complete := true
	for i := range 2 {
		if included(i) && positions[i] != size(i) {
			complete = false
		}
	}
	return ReadPage{Text: frame(), Position: positions, Complete: complete}, nil
}
