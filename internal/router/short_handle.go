package router

import (
	"context"
	"errors"
	"strconv"
)

// The vocabulary is protocol data: never reorder or remove words. Handles are
// locators, not integrity hashes. Owners retain their original identity checks.
var handleWords = [...]string{
	"amber", "apple", "arch", "ash", "atlas", "beach", "birch", "bird",
	"bloom", "blue", "brook", "cloud", "coral", "dawn", "delta", "dove",
	"elm", "fern", "field", "flame", "flint", "forest", "frost", "glass",
	"gold", "green", "grove", "hill", "iris", "jade", "lake", "leaf",
	"light", "lily", "lime", "maple", "mint", "moon", "moss", "oak",
	"ocean", "olive", "opal", "pearl", "pine", "plum", "pond", "rain",
	"reed", "river", "rose", "ruby", "sage", "sand", "sky", "snow",
	"star", "stone", "sun", "tide", "trail", "tree", "wave", "wind",
}

func shortHandle(number uint64) string {
	word := handleWords[number%uint64(len(handleWords))]
	if cycle := number / uint64(len(handleWords)); cycle != 0 {
		word += strconv.FormatUint(cycle, 10)
	}
	return word
}

func parseShortHandle(handle string) (uint64, bool) {
	end := 0
	for end < len(handle) && handle[end] >= 'a' && handle[end] <= 'z' {
		end++
	}
	for index, word := range handleWords {
		if handle[:end] != word {
			continue
		}
		var cycle uint64
		if end != len(handle) {
			var err error
			cycle, err = strconv.ParseUint(handle[end:], 10, 64)
			if err != nil || cycle == 0 || strconv.FormatUint(cycle, 10) != handle[end:] {
				return 0, false
			}
		}
		if cycle > (^uint64(0)-uint64(index))/uint64(len(handleWords)) {
			return 0, false
		}
		return cycle*uint64(len(handleWords)) + uint64(index), true
	}
	return 0, false
}

// Callers already holding store.lock use the locked variant.
func (s *mekugiReplayStore) allocateHandles(ctx context.Context, count int) ([]string, error) {
	if s == nil {
		return nil, errors.New("handle allocation storage is unavailable")
	}
	s = s.scoped(ctx)
	var handles []string
	err := s.locked(ctx, func() error {
		var err error
		handles, err = s.allocateHandlesLocked(count)
		return err
	})
	return handles, err
}

func (s *mekugiReplayStore) allocateHandlesLocked(count int) ([]string, error) {
	if count < 1 {
		return nil, errors.New("handle allocation must be nonempty")
	}
	scope, _, err := s.readHandleScope(s.handleNamespace())
	if err != nil {
		return nil, err
	}
	if scope.Namespace != s.handleNamespace() {
		return nil, errors.New("handle allocation requires the root namespace")
	}
	next := scope.Next
	if uint64(count) > ^uint64(0)-next {
		return nil, errors.New("handle allocation counter exhausted")
	}
	handles := make([]string, count)
	for index := range handles {
		handles[index] = shortHandle(next + uint64(index))
	}
	scope.Next += uint64(count)
	if err := s.writeHandleScope(scope); err != nil {
		return nil, err
	}
	return handles, nil
}
