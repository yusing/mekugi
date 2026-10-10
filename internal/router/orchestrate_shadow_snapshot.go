package router

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// Durable branches use the snapshot policy, not its disposable observation store.
func orchestrateShadowSnapshot(ctx context.Context, workspace, repository string) (string, error) {
	return orchestrateSnapshot(ctx, workspace, repository, false)
}

func orchestrateSnapshot(ctx context.Context, workspace, repository string, committed bool) (string, error) {
	r := &workspaceSnapshotRepo{
		owner:     &workspaceSnapshots{directory: filepath.Dir(repository)},
		workspace: workspace, gitDir: repository, index: repository + ".index",
		lockPath: repository + ".lock", gate: make(chan struct{}, 1), durable: true,
		committed: committed,
	}
	tree, err := r.snapshot(ctx)
	if err != nil {
		return "", err
	}
	// A gitlink would omit nested checkout content from this baseline.
	entries, err := r.run(ctx, nil, "ls-tree", "-r", "--format=%(objectmode)", tree)
	if err != nil {
		return "", err
	}
	if strings.Contains(string(entries), "160000") {
		return "", errors.New("shadow snapshot contains a nested Git checkout")
	}
	commit, err := r.run(ctx, nil, "-c", "user.name=Mekugi", "-c", "user.email=orchestrate@mekugi.invalid", "-c", "commit.gpgSign=false", "commit-tree", tree, "-m", "Shadow baseline")
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(string(commit), "\n")
	_, err = r.run(ctx, nil, "update-ref", "refs/mekugi/snapshots/"+base, base)
	return base, err
}
