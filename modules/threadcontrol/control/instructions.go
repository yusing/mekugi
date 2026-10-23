package control

import (
	"context"
	"errors"
	"slices"

	"github.com/yusing/mekugi/contracts/guidance"
)

// Provenance validation and publication share the same mutation admission.
func (s *Service) RetainInstructions(ctx context.Context, thread, path string, fragments []guidance.Fragment) error {
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		changed := member.Path != path
		member.Instructions = slices.Clone(member.Instructions)
		for _, part := range fragments {
			if part.Thread != thread {
				return errors.New("instruction source conflicts with admitted thread")
			}
			index := slices.IndexFunc(member.Instructions, func(old guidance.Fragment) bool { return old.Item == part.Item && old.Index == part.Index })
			if index >= 0 {
				if member.Instructions[index] != part {
					return errors.New("conflicting retained instruction provenance")
				}
			} else {
				member.Instructions = append(member.Instructions, part)
				changed = true
			}
		}
		if !changed {
			return unchanged
		}
		member.Path = path
		return nil
	})
}
