package paging

import (
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"
)

// Source owns one operation's validated streams. JSON admission counts entries
// without retaining a decoded copy of every entry, including unselected streams.
type Source struct {
	values [2]string
	kinds  [2]string
	sizes  [2]int
}

func Admit(stdout, stderr, stdoutKind, stderrKind string) (*Source, error) {
	s := &Source{values: [2]string{stdout, stderr}, kinds: [2]string{stdoutKind, stderrKind}}
	for i := range 2 {
		s.sizes[i] = len(s.values[i])
		if s.kinds[i] == "json" {
			var err error
			s.sizes[i], err = CountArray(s.values[i], false)
			if err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

// CountArray preserves legacy publication validation separately from strict
// paging admission. The decoder validates complete values without copying them.
func CountArray(text string, legacy bool) (int, error) {
	d := jsontext.NewDecoder(strings.NewReader(text), jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(legacy))
	token, err := d.ReadToken()
	if err != nil || token.Kind() != '[' {
		return 0, errors.New("structured read output must be a JSON array")
	}
	count := 0
	for d.PeekKind() != ']' {
		if err := d.SkipValue(); err != nil {
			return 0, err
		}
		count++
	}
	if _, err := d.ReadToken(); err != nil {
		return 0, err
	}
	if _, err := d.ReadToken(); err != io.EOF {
		return 0, errors.New("structured read output must be a JSON array")
	}
	return count, nil
}

// entries retains only the candidates a bounded page can inspect. Offsets and
// raw entry slices refer to the admitted string, preserving whitespace/escapes.
func (s *Source) entries(i, offset, budget int) []string {
	d := jsontext.NewDecoder(strings.NewReader(s.values[i]), jsontext.AllowInvalidUTF8(true))
	_, _ = d.ReadToken() // Admission has already validated this complete array.
	for range offset {
		_ = d.SkipValue()
	}
	var entries []string
	bytes := 2
	for offset+len(entries) < s.sizes[i] && bytes <= budget*128 {
		start := int(d.InputOffset())
		_ = d.SkipValue()
		end := int(d.InputOffset())
		entry := strings.TrimSpace(s.values[i][start:end])
		entry = strings.TrimSpace(strings.TrimPrefix(entry, ","))
		entries = append(entries, entry)
		bytes += len(entry) + 1
	}
	return entries
}
