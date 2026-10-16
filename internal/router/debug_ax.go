package router

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/rollout"
)

func discoverDebugRollouts(ctx context.Context, threads []string) (map[string][]string, error) {
	found := make(map[string][]string)
	if len(threads) == 0 {
		return found, nil
	}
	codexDirectory := os.Getenv("CODEX_HOME")
	if codexDirectory == "" {
		userDirectory, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		codexDirectory = filepath.Join(userDirectory, ".codex")
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
			if visited > 100000 {
				return errors.New("rollout discovery exceeds entry bound")
			}
			if entry.Type().IsRegular() && slices.ContainsFunc(threads, func(id string) bool {
				return strings.HasSuffix(entry.Name(), "-"+id+".jsonl")
			}) {
				// Filenames only select candidates: "other-thread" also ends
				// in "thread". Attribute by the rollout's exact metadata ID.
				id, err := rollout.HeaderIdentity(path)
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
