package projection

import "github.com/yusing/mekugi/libraries/outputbudget/tokenizer"

// Savings measures changed decoded string leaves. Projection preserves all
// other keys and scalars, so their independent token counts cancel exactly.
// The codec and accumulated estimate belong only to this request.
type Savings struct {
	codec  *tokenizer.Codec
	saved  int64
	failed bool
}

func (s *Savings) Add(before, after string) {
	if before == after || s.failed {
		return
	}
	original, projected := s.count(before), s.count(after)
	s.saved += original - projected
}

func (s *Savings) count(text string) int64 {
	if s.failed {
		return 0
	}
	if s.codec == nil {
		var err error
		s.codec, err = tokenizer.New()
		if err != nil {
			s.failed = true
			return 0
		}
	}
	count, err := s.codec.Count(text)
	if err != nil {
		s.failed = true
		return 0
	}
	return int64(count)
}

// Result preserves projection when auxiliary measurement is unavailable.
func (s *Savings) Result() (saved int64, measured bool) {
	if s.failed {
		return 0, false
	}
	return s.saved, true
}
