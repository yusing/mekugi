package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// ShadowMerge retains a source preimage and merged result before writeback.
type ShadowMerge struct {
	State     string        `json:"state"`
	Tip       string        `json:"tip"`
	SourceTip string        `json:"source_tip"`
	Tree      string        `json:"tree"`
	Paths     []string      `json:"paths,omitempty"`
	Conflicts []string      `json:"conflicts,omitempty"`
	Writes    []ShadowWrite `json:"writes,omitempty"`
}

// PlanShadowMerge writes only run-owned objects and the manifest. The router
// supplies confirmed live coordinator and child admission before calling it.
func (s *Store) PlanShadowMerge(ctx context.Context, workspace, main, name, thread string) (batch Batch, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		index := slices.IndexFunc(m.Batches, func(b Batch) bool { return b.TaskName == name })
		if index < 0 {
			return errors.New("shadow merge target is not in this run")
		}
		b := &m.Batches[index]
		batch = *b
		if !shadowVCS(b.VCS) || b.State != "launched" || b.Launch == nil || b.Launch.ThreadID != thread || thread == "" {
			return errors.New("shadow merge requires a confirmed shadow batch")
		}
		for _, d := range m.Deliveries {
			if (d.Target == thread || d.From == thread) && (d.State == "queued" || d.State == "dispatching" || d.State == "uncertain") {
				return errors.New("shadow merge requires settled batch deliveries")
			}
		}
		if err := validateCheckoutIdentity(ctx, *b); err != nil {
			return err
		}
		if err := validateIndexFlags(ctx, b.Checkout); err != nil {
			return err
		}
		status, err := git(ctx, b.Cwd, "status", "--porcelain", "--untracked-files=all")
		if err != nil || status != "" {
			return errors.New("shadow merge requires a clean batch checkout")
		}
		vcs, source, err := sourceRepository(ctx, workspace)
		if err != nil || vcs != b.VCS || !filepath.IsAbs(b.Source) || b.Source != source {
			return errors.New("shadow source identity changed or is unavailable")
		}
		if s.ShadowSnapshot == nil {
			return errors.New("shadow snapshot owner is unavailable")
		}
		if b.ShadowMerge != nil && b.ShadowMerge.State != "planned" && b.ShadowMerge.State != "conflicted" && b.ShadowMerge.State != "applied" {
			return errors.New("unfinished shadow writeback requires recovery")
		}
		tip, err := sourceBase(ctx, b.Cwd)
		if err != nil {
			return err
		}
		sourceTip, err := s.ShadowSnapshot(ctx, source, b.Repository)
		if err != nil {
			return err
		}
		emptyTree, err := shadowGit(ctx, b.Repository, "hash-object", "-w", "-t", "tree", "--stdin")
		if err != nil {
			return err
		}
		// Snapshot bytes, not checkout attributes or custom merge drivers,
		// define the source comparison. Git still detects binary conflicts.
		base := b.Base
		if b.Integration != nil {
			base = b.Integration.Tip // Keep the last confirmed base through conflicts.
		}
		out, mergeErr := shadowGit(ctx, b.Repository, "--attr-source="+strings.TrimSpace(string(emptyTree)), "merge-tree", "--write-tree", "--merge-base="+base, "-z", "--name-only", "--no-messages", tip, sourceTip)
		var exit *exec.ExitError
		conflicted := errors.As(mergeErr, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil
		if mergeErr != nil && !conflicted {
			return mergeErr
		}
		parts := strings.Split(string(out), "\x00")
		if len(parts) < 2 || parts[0] == "" {
			return errors.New("shadow merge returned no tree")
		}
		plan := &ShadowMerge{State: "planned", Tip: tip, SourceTip: sourceTip, Tree: parts[0]}
		if conflicted {
			plan.State = "conflicted"
			for _, name := range parts[1:] {
				if name == "" {
					break
				}
				plan.Conflicts = append(plan.Conflicts, name)
			}
		} else {
			changed, err := shadowGit(ctx, b.Repository, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", sourceTip, plan.Tree)
			if err != nil {
				return err
			}
			if len(changed) != 0 {
				plan.Paths = strings.Split(strings.TrimSuffix(string(changed), "\x00"), "\x00")
			}
		}
		// The manifest's tree name alone does not protect merged blobs from
		// private-repository GC. Retain the result before publishing its plan.
		if _, err := shadowGit(ctx, b.Repository, "update-ref", "refs/mekugi/merges/"+plan.Tree, plan.Tree); err != nil {
			return err
		}
		b.ShadowMerge = plan
		if err := s.save(m, path); err != nil {
			return err
		}
		batch = *b
		return nil
	})
	return batch, err
}

// Preserve stdout on merge-tree's conflict exit. Private merges do not use
// inherited Git selection, user hooks, filters or configured merge drivers.
func shadowGit(ctx context.Context, repository string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + repository,
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.autocrlf=false", "-c", "merge.default=text", "-c", "gc.auto=0"}, args...)...)
	command.Env = append(slices.DeleteFunc(os.Environ(), func(v string) bool { return strings.HasPrefix(v, "GIT_") }),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out, fmt.Errorf("shadow git %s: %w: %s", args[0], err, strings.TrimSpace(string(exit.Stderr)))
		}
	}
	return out, err
}
