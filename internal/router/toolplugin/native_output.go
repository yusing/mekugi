package toolplugin

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type selectedNativeText struct {
	Text   string `json:"text"`
	Tokens int    `json:"tokens"`
}

// Source: plugins/mrun.ts:17:85@[543de4f3] selectMRunText/formatMRunOutput
func (b *nativeBudget) selectText(value string, budget int, tail bool) (selectedNativeText, error) {
	if budget <= 0 {
		return selectedNativeText{}, nil
	}
	if _, err := b.count(""); err != nil {
		return selectedNativeText{}, err
	}
	_, tokens, err := b.codec.Encode(value)
	if err != nil {
		return selectedNativeText{}, err
	}
	if len(tokens) <= budget {
		return selectedNativeText{value, len(tokens)}, nil
	}
	for keep := budget; keep > 0; {
		parts := tokens[:keep]
		if tail {
			parts = tokens[len(tokens)-keep:]
		}
		s := strings.Join(parts, "")
		for len(s) > 0 && !utf8.ValidString(s) {
			if tail {
				s = s[1:]
			} else {
				s = s[:len(s)-1]
			}
		}
		n, e := b.count(s)
		if e != nil {
			return selectedNativeText{}, e
		}
		if n <= budget {
			return selectedNativeText{s, n}, nil
		}
		keep -= max(1, n-budget)
	}
	return selectedNativeText{}, nil
}
func formatNativeOutput(ctx context.Context, args []string) (ExecutionOutput, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionOutput{}, err
	}
	if len(args) != 4 {
		return ExecutionOutput{}, errors.New("invalid mrun output selection")
	}
	budget, err := strconv.Atoi(args[0])
	if err != nil || budget < 1 || budget > nativeMaxTokens || strconv.Itoa(budget) != args[0] {
		return ExecutionOutput{}, errors.New("invalid mrun output selection")
	}
	counter := nativeBudget{}
	mode, stdout, stderr := args[1], args[2], args[3]
	if mode == "read" {
		var request nativeReadRequest
		if err := json.Unmarshal([]byte(stdout), &request); err != nil {
			return ExecutionOutput{}, err
		}
		page, err := counter.readPage(request, budget)
		return ExecutionOutput{Stdout: nativeJSON(page)}, err
	}
	if mode == "shell" {
		selected, e := counter.selectText(stdout, budget, false)
		if e != nil {
			return ExecutionOutput{}, e
		}
		framed := func(s string) (int, error) {
			if s == "" {
				return 0, nil
			}
			return counter.count(nativeJSON(nativeJSON(s)))
		}
		for {
			n, e := framed(selected.Text)
			if e != nil {
				return ExecutionOutput{}, e
			}
			if n <= budget {
				break
			}
			selected, e = counter.selectText(selected.Text, max(0, selected.Tokens*budget/n-1), false)
			if e != nil {
				return ExecutionOutput{}, e
			}
		}
		if len(selected.Text) < len(stdout) {
			selected.Text = selected.Text[:strings.LastIndexByte(selected.Text, '\n')+1]
		}
		selected.Tokens, err = framed(selected.Text)
		return ExecutionOutput{Stdout: nativeJSON(selected)}, err
	}
	if mode != "head" && mode != "tail" && mode != "rows" {
		return ExecutionOutput{}, errors.New("invalid mrun output selection")
	}
	selectValue := func(value string, limit int) (selectedNativeText, error) {
		s, e := counter.selectText(value, limit, mode == "tail")
		if e == nil && mode == "rows" && len(s.Text) < len(value) {
			s.Text = s.Text[:strings.LastIndexByte(s.Text, '\n')+1]
			s.Tokens, e = counter.count(s.Text)
		}
		return s, e
	}
	errorBudget := budget
	if stdout != "" {
		errorBudget = budget / 2
	}
	errorText, e := selectValue(stderr, errorBudget)
	if e != nil {
		return ExecutionOutput{}, e
	}
	output, e := selectValue(stdout, budget-errorText.Tokens)
	return ExecutionOutput{Stdout: output.Text, Stderr: errorText.Text}, e
}

type nativeReadRequest struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	StdoutKind string `json:"stdoutKind"`
	StderrKind string `json:"stderrKind"`
	Position   [2]int `json:"position"`
	Stream     string `json:"stream"`
	SourceRow  int    `json:"sourceRow"`
	Label      string `json:"label"`
}
type nativeReadPage struct {
	Text         string `json:"text"`
	Position     [2]int `json:"position"`
	Complete     bool   `json:"complete"`
	NeededTokens int    `json:"neededTokens,omitzero"`
}

// Source: plugins/read_output.ts:44:134@[543de4f3] selectReadOutput
func (b *nativeBudget) readPage(req nativeReadRequest, budget int) (nativeReadPage, error) {
	values := [2]string{req.Stdout, req.Stderr}
	kinds := [2]string{req.StdoutKind, req.StderrKind}
	positions := req.Position
	pages := [2]string{}
	var entries [2][]jsontext.Value
	for i := range 2 {
		if kinds[i] == "json" {
			if err := json.Unmarshal([]byte(values[i]), &entries[i], jsontext.AllowInvalidUTF8(true)); err != nil {
				return nativeReadPage{}, err
			}
			if strings.TrimSpace(values[i]) == "null" {
				return nativeReadPage{}, errors.New("structured read output must be a JSON array")
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
			return nativeReadPage{}, errors.New("invalid read position")
		}
	}
	sourceStart := req.SourceRow
	if sourceStart > 0 {
		sourceStart += strings.Count(req.Stdout[:req.Position[0]], "\n")
	}
	frame := func() string {
		prefix := ""
		if req.Label != "" {
			prefix = "--- " + req.Label + " ---\n"
		}
		if pages[0] != "" && sourceStart > 0 {
			prefix += fmt.Sprintf("[rows %d:%d]\n", sourceStart, sourceStart+strings.Count(pages[0], "\n")-1)
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
	needed := func() (nativeReadPage, error) {
		n, e := b.count(frame())
		return nativeReadPage{Position: req.Position, NeededTokens: n}, e
	}
	for i := range 2 {
		if included(i) && kinds[i] == "json" && len(entries[i]) == 0 {
			pages[i] = "[]"
		}
	}
	fit, err := b.fits(frame(), budget)
	if err != nil {
		return nativeReadPage{}, err
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
				fit, e := b.fits(frame(), budget)
				if e != nil {
					return nativeReadPage{}, e
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
				return nativeReadPage{}, errors.New("read position splits UTF-8")
			}
			if kinds[i] == "rows" && offset > 0 && values[i][offset-1] != '\n' {
				return nativeReadPage{}, errors.New("read position splits a complete row")
			}
			end := min(len(values[i]), offset+budget*128+4)
			for end < len(values[i]) && !utf8.RuneStart(values[i][end]) {
				end--
			}
			remaining := values[i][offset:end]
			allowance := budget
			for allowance > 0 {
				selected, e := b.selectText(remaining, allowance, false)
				if e != nil {
					return nativeReadPage{}, e
				}
				pages[i] = selected.Text
				if kinds[i] == "rows" && (len(pages[i]) < len(remaining) || end < len(values[i])) {
					pages[i] = pages[i][:strings.LastIndexByte(pages[i], '\n')+1]
				}
				n, e := b.count(frame())
				if e != nil {
					return nativeReadPage{}, e
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
	return nativeReadPage{Text: frame(), Position: positions, Complete: complete}, nil
}
