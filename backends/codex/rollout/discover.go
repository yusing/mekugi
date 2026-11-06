package rollout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// maxDiscoveryEntries bounds one discovery walk across sessions and archives.
const maxDiscoveryEntries = 100000

// Discover finds rollouts for exact thread identities under the host's sessions
// and archived_sessions directories. Filenames only select candidates; each
// candidate's metadata header establishes its identity.
func Discover(ctx context.Context, threads []string) (map[string][]string, error) {
	found := make(map[string][]string)
	if len(threads) == 0 {
		return found, nil
	}
	codexDirectory, err := HomeDirectory()
	if err != nil {
		return nil, err
	}
	visited := 0
	for _, name := range []string{"sessions", "archived_sessions"} {
		directory := filepath.Join(codexDirectory, name)
		err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				if path == directory && errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			visited++
			if visited > maxDiscoveryEntries {
				return errors.New("rollout discovery exceeds entry bound")
			}
			if entry.Type().IsRegular() && slices.ContainsFunc(threads, func(id string) bool {
				return strings.HasSuffix(entry.Name(), "-"+id+".jsonl")
			}) {
				// "other-thread" also ends in "thread"; attribute by exact metadata ID.
				id, err := HeaderIdentity(path)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				if slices.Contains(threads, id) {
					found[id] = append(found[id], path)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}

// DiscoverOne resolves exactly one rollout for a thread identity.
func DiscoverOne(ctx context.Context, thread string) (string, error) {
	rollouts, err := Discover(ctx, []string{thread})
	if err != nil {
		return "", err
	}
	switch paths := rollouts[thread]; len(paths) {
	case 0:
		return "", fmt.Errorf("rollout for %q not found in Codex sessions or archived_sessions", thread)
	case 1:
		return paths[0], nil
	default:
		return "", fmt.Errorf("%q has multiple rollouts in Codex sessions or archived_sessions", thread)
	}
}
