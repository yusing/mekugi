package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const integrationNativePath = "runtime/codex"

func integrationPin(root, commit string) (string, error) {
	entry, err := integrationGit(root, "ls-tree", "--format=%(objectmode) %(objectname)", commit, "--", integrationNativePath)
	if err != nil || entry == "" {
		return "", err
	}
	pin, ok := strings.CutPrefix(entry, "160000 ")
	if !ok {
		return "", fmt.Errorf("%s in %s is not a native gitlink", integrationNativePath, commit)
	}
	return pin, nil
}

func verifyIntegrationNative(root, pin string) (string, error) {
	native := filepath.Join(root, integrationNativePath)
	info, err := os.Lstat(native)
	if pin == "" || err != nil || !info.IsDir() {
		return "", fmt.Errorf("initialize %s at the committed native pin before checking integration", integrationNativePath)
	}
	top, err := integrationGit(native, "rev-parse", "--show-toplevel")
	if err != nil || top != native {
		return "", fmt.Errorf("%s is not an initialized native checkout", native)
	}
	head, err := integrationGit(native, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if head != pin {
		return "", fmt.Errorf("native HEAD %s does not match Mekugi pin %s; reconcile %s before checking integration", head, pin, native)
	}
	status, err := integrationGit(native, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("native checkout %s must be clean before accepting integration checks", native)
	}
	return native, nil
}

func integrationIdle(root string) error {
	args := []string{"rev-parse", "--path-format=absolute"}
	for _, entry := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_START", "rebase-merge", "rebase-apply", "sequencer"} {
		args = append(args, "--git-path", entry)
	}
	paths, err := integrationGit(root, args...)
	if err != nil {
		return err
	}
	for path := range strings.SplitSeq(paths, "\n") {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("Git operation in progress in %s (%s); resolve it before integration", root, filepath.Base(path))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func integrationAncestor(root, ancestor, commit string) (bool, error) {
	_, err := integrationGit(root, "merge-base", "--is-ancestor", ancestor, commit)
	if status, ok := errors.AsType[*exec.ExitError](err); ok && status.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

func integrationMergeCommand(root, ref string, options ...string) error {
	args := []string{"-c", "submodule.recurse=false", "merge", "--no-edit", "--no-squash", "--no-autostash", "--no-overwrite-ignore", "--no-rerere-autoupdate"}
	args = append(args, options...)
	return integrationCommand(root, "git", append(args, "--", ref)...)
}

// Some merge strategies overwrite ignored files even with --no-overwrite-ignore.
// Compare path components, including file/directory collisions, before either merge.
func protectIntegrationLocalFiles(root, incoming string) error {
	local, err := integrationGitOutput(root, "ls-files", "--others", "--directory", "--no-empty-directory", "-z")
	if err != nil || local == "" {
		return err
	}
	tree, err := integrationGitOutput(root, "ls-tree", "-r", "--name-only", "-z", incoming)
	if err != nil {
		return err
	}
	paths := strings.Split(strings.TrimSuffix(local, "\x00"), "\x00")
	for i, name := range paths {
		paths[i] = strings.TrimSuffix(name, "/")
	}
	slices.Sort(paths)
	for name := range strings.SplitSeq(strings.TrimSuffix(tree, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		if i, _ := slices.BinarySearch(paths, name+"/"); i < len(paths) && strings.HasPrefix(paths[i], name+"/") {
			return fmt.Errorf("incoming path %q overlaps local untracked path %q in %s", name, paths[i], root)
		}
		for candidate := name; candidate != "."; candidate = path.Dir(candidate) {
			if _, found := slices.BinarySearch(paths, candidate); found {
				return fmt.Errorf("incoming path %q overlaps local untracked path %q in %s", name, candidate, root)
			}
		}
	}
	return nil
}

func mergeIntegration(root, ref string) (_ string, err error) {
	if err := integrationIdle(root); err != nil {
		return "", err
	}
	incoming, err := integrationGit(root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if err := protectIntegrationLocalFiles(root, incoming); err != nil {
		return "", err
	}
	oldPin, err := integrationPin(root, "HEAD")
	if err != nil {
		return "", err
	}
	incomingPin, err := integrationPin(root, incoming)
	if err != nil {
		return "", err
	}
	if incomingPin == "" && oldPin == "" {
		return "", integrationMergeCommand(root, incoming, "--no-ff")
	}
	bases, err := integrationGit(root, "merge-base", "--all", "HEAD", incoming)
	if err != nil {
		return "", fmt.Errorf("cannot find a common Mekugi merge base for %s: %v", incoming, err)
	}
	mergeBases := strings.Fields(bases)
	basePin, err := integrationPin(root, mergeBases[0])
	if err != nil {
		return "", err
	}
	for _, base := range mergeBases[1:] {
		pin, err := integrationPin(root, base)
		if err != nil {
			return "", err
		}
		if pin != basePin {
			return "", errors.New("Mekugi merge bases disagree on the native pin; integrate manually")
		}
	}
	if basePin == incomingPin {
		// A source branch without a gitlink change may simply predate a pin bump.
		return "", integrationMergeCommand(root, incoming, "--no-ff")
	}
	if oldPin == "" || incomingPin == "" || basePin == "" {
		return "", errors.New("adding or removing the native gitlink requires manual integration")
	}
	native, err := verifyIntegrationNative(root, oldPin)
	if err != nil {
		return "", err
	}
	for _, directory := range []string{root, native} {
		branch, err := integrationGit(directory, "branch", "--show-current")
		if err != nil {
			return "", err
		}
		if branch != "main" {
			return "", fmt.Errorf("paired integration requires main in %s", directory)
		}
	}
	if err := integrationIdle(native); err != nil {
		return "", err
	}
	rewind, err := integrationAncestor(native, incomingPin, basePin)
	if err != nil {
		return "", fmt.Errorf("could not verify incoming native pin %s against base %s in %s; make both commits available locally: %v", incomingPin, basePin, native, err)
	}
	if rewind {
		return "", errors.New("incoming Mekugi ref rewinds the native pin; integrate manually")
	}
	if err := protectIntegrationLocalFiles(native, incomingPin); err != nil {
		return "", err
	}

	defer func() {
		if err != nil {
			reportIntegrationMerge(root, native, incoming, incomingPin)
		}
	}()
	if err := integrationMergeCommand(native, incomingPin, "--ff", "--commit"); err != nil {
		return "", err
	}
	nativeHead, err := integrationGit(native, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	// Even a fast-forward-eligible Mekugi branch may need a different gitlink.
	mergeErr := integrationMergeCommand(root, incoming, "--no-ff", "--no-commit")
	mergeHead, err := integrationGit(root, "rev-parse", "--verify", "-q", "MERGE_HEAD")
	if mergeHead != incoming || err != nil {
		if mergeErr != nil {
			return "", mergeErr
		}
		return "", errors.New("Mekugi merge did not stop at the reviewed ref; inspect both repositories")
	}
	if mergeErr != nil {
		unmerged, err := integrationGitOutput(root, "diff", "--name-only", "--diff-filter=U", "-z", "--ignore-submodules=none")
		if err != nil || unmerged != integrationNativePath+"\x00" {
			return "", mergeErr
		}
	}
	if err := integrationCommand(root, "git", "update-index", "--cacheinfo", "160000,"+nativeHead+","+integrationNativePath); err != nil {
		return "", err
	}
	if err := integrationCommand(root, "git", "commit", "--no-edit"); err != nil {
		return "", err
	}
	return nativeHead, nil
}

func reportIntegrationMerge(root, native, incoming, incomingPin string) {
	for _, repository := range []struct{ name, directory, input string }{
		{"Mekugi", root, incoming}, {"native", native, incomingPin},
	} {
		head, _ := integrationGit(repository.directory, "rev-parse", "HEAD")
		mergeHead, _ := integrationGit(repository.directory, "rev-parse", "--verify", "-q", "MERGE_HEAD")
		fmt.Fprintf(os.Stderr, "%s %s: HEAD %s, input %s", repository.name, repository.directory, head, repository.input)
		if mergeHead != "" {
			fmt.Fprintf(os.Stderr, ", merge in progress at %s", mergeHead)
		}
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintln(os.Stderr, "Integration stopped. Inspect both Git states and finish or abort any retained merge manually before retrying; no checks or finalization ran.")
}
