package control

import (
	"context"
	"errors"
	"slices"
)

// Rewind replaces only the human-controlled root of an existing live family.
// Previous roots remain as historical effect endpoints, without caller authority.
// The retained coordination checkpoint and native descendants keep their identities.
func (s *Service) Rewind(ctx context.Context, original string, next ThreadInfo) error {
	if next.Thread == "" || next.Thread == original || next.Session == "" || next.Path == "" {
		return errors.New("rewind requires a distinct confirmed native root")
	}
	return s.update(ctx, original, func(tree *Tree, root *Member) error {
		if tree.Root == next.Thread && tree.Members[next.Thread].Session == next.Session {
			return unchanged
		}
		if tree.Root != original || root.Parent != "" || root.HostRunning {
			return errors.New("rewind requires the idle admitted root")
		}
		if tree.Members[next.Thread] != nil {
			return errors.New("rewind identity is already a family member")
		}
		replacement := *root
		replacement.Thread, replacement.Session, replacement.Path = next.Thread, next.Session, next.Path
		replacement.Turn, replacement.LastAnswer = "", ""
		replacement.Wait, replacement.RecoveryWait, replacement.Stop = nil, nil, nil
		replacement.Instructions = slices.Clone(root.Instructions)
		for i := range replacement.Instructions {
			replacement.Instructions[i].Thread = next.Thread
		}
		tree.Members[next.Thread] = &replacement
		return tree.selectRoot(next.Thread)
	})
}

// selectRoot is also used when explicitly resuming a previous rewind branch.
// It never clones native descendants or changes their work generations.
func (tree *Tree) selectRoot(thread string) error {
	root := tree.Members[thread]
	if root == nil || root.Parent != "" || root.Task != "/root" {
		return errors.New("selected session is not a retained root branch")
	}
	if tree.Root == thread {
		return nil
	}
	previous := tree.edit(tree.Root)
	if previous.HostRunning {
		return errors.New("the family's current root is still running")
	}
	previous.AudienceAdmitted = false
	tree.edit(thread).AudienceAdmitted = true
	for id, child := range tree.Members {
		if child.Parent == tree.Root {
			tree.edit(id).Parent = thread
		}
	}
	for i := range tree.Effects {
		effect := &tree.Effects[i]
		if effect.Target == tree.Root && (effect.Kind == "message" || effect.Kind == "final") && (effect.State == "pending" || effect.State == "queued") {
			effect.Target = thread
		}
	}
	tree.Root = thread
	return nil
}
