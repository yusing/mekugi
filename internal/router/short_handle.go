package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// The constant-sized high-water mark is store metadata, like store.lock. It is
// not session data and must outlive reclaimed records so an old handle can never
// name a newly allocated object. Callers already holding store.lock use the
// locked variant.
func (s *mekugiReplayStore) allocateHandles(ctx context.Context, count int) ([]string, error) {
	if s == nil {
		return nil, errors.New("handle allocation storage is unavailable")
	}
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
	const name = "handle-counter"
	data, err := readManagedOutputFile(filepath.Join(s.directory, name))
	var next uint64
	if err == nil {
		value := strings.TrimSuffix(string(data), "\n")
		next, err = strconv.ParseUint(value, 10, 64)
		if err != nil || strconv.FormatUint(next, 10) != value {
			return nil, errors.New("invalid handle allocation counter")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if uint64(count) > ^uint64(0)-next {
		return nil, errors.New("handle allocation counter exhausted")
	}
	handles := make([]string, count)
	for index := range handles {
		handles[index] = shortHandle(next + uint64(index))
	}
	if err := s.writeFile(name, "handle-counter-pending-", []byte(fmt.Sprintf("%d\n", next+uint64(count)))); err != nil {
		return nil, storageIOError(err)
	}
	return handles, nil
}

// Proxies without replay storage have only process-local history. The same
// lifetime applies to their handles; durable proxies always use the disk counter.
func (p *mekugiProxy) allocateHandles(ctx context.Context, count int) ([]string, error) {
	if p.replayStore != nil {
		return p.replayStore.allocateHandles(ctx, count)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if count < 1 || uint64(count) > ^uint64(0)-p.nextHandle {
		return nil, errors.New("invalid handle allocation count")
	}
	handles := make([]string, count)
	for index := range handles {
		handles[index] = shortHandle(p.nextHandle)
		p.nextHandle++
	}
	return handles, nil
}
