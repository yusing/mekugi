package control

import (
	"errors"
)

// selectRoot admits a saved root branch, including branches retained by older sessions.
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
