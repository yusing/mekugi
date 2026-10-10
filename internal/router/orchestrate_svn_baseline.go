package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Revert only the copied metadata. This preserves per-path BASE revisions and
// restores replaced or moved nodes that svn export -r BASE cannot reconstruct.
func orchestrateSVNBaseline(ctx context.Context, source, repository string) (string, error) {
	client, err := exec.LookPath("svn")
	if err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp("", "mekugi-svn-baseline-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	metadata := filepath.Join(temporary, ".svn")
	if err := os.CopyFS(metadata, os.DirFS(filepath.Join(source, ".svn"))); err != nil {
		return "", fmt.Errorf("copy SVN metadata: %w", err)
	}
	command := exec.CommandContext(ctx, client, "revert", "--non-interactive", "--recursive", "--", temporary)
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("restore SVN baseline: %w: %s", err, strings.TrimSpace(string(output)))
	}
	// The imported tree contains versioned bytes only, never admin metadata.
	if err := os.RemoveAll(metadata); err != nil {
		return "", err
	}
	return orchestrateSnapshot(ctx, temporary, repository, true)
}
