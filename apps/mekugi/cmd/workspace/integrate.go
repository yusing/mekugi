package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func integrate(args []string) error {
	flags := flag.NewFlagSet("integrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	commits := flags.String("commits", "", "space-separated commits in cherry-pick order")
	items := flags.String("items", "", "space-separated Plane items")
	state := flags.String("state", "", "Main-selected Plane state")
	checked := flags.String("checked-head", "", "Main attests prior checks at this full HEAD")
	accept := flags.Bool("accept", false, "Main authorizes the requested status and cleanup")
	var cleanup []string
	flags.Func("cleanup", "remove this verified source worktree and branch; repeatable", func(path string) error { cleanup = append(cleanup, path); return nil })
	if err := flags.Parse(args); err != nil {
		return err
	}
	finalize := *state != "" || len(cleanup) > 0
	if (len(strings.Fields(*items)) == 0) != (*state == "") || finalize && (!*accept || flags.NArg() == 0 && *checked == "") {
		return errors.New("status/cleanup require Main --accept and check argv or --checked-head; --items and --state must be paired")
	}
	if *checked != "" && (*commits != "" || flags.NArg() > 0) {
		return errors.New("--checked-head reuses prior checks; do not combine it with new commits or check argv")
	}
	root, err := integrationGit(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if _, err := integrationGit(root, "diff", "--cached", "--quiet"); err != nil {
		return errors.New("staged index must be resolved before integration; unrelated work was not staged or discarded")
	}
	var picks []string
	for ref := range strings.FieldsSeq(*commits) {
		hash, err := integrationGit(root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		if err != nil {
			return err
		}
		picks = append(picks, hash)
	}
	if len(picks) > 0 {
		if err := integrationCommand(root, "git", append([]string{"cherry-pick"}, picks...)...); err != nil {
			return err
		}
	}
	head, err := integrationGit(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if *checked != "" && *checked != head {
		return errors.New("Main's checked HEAD does not match the current full HEAD")
	}
	if flags.NArg() > 0 {
		if err := integrationCommand(root, flags.Arg(0), flags.Args()[1:]...); err != nil {
			return err
		}
	}
	current, err := integrationGit(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if current != head {
		return errors.New("HEAD changed during checks; no status or cleanup performed")
	}
	fmt.Fprintf(os.Stdout, "Integration HEAD: %s. No acceptance or check coverage inferred.\n", head)
	for item := range strings.FieldsSeq(*items) {
		if err := integrationCommand(root, "plane", "update", item, "--state", *state); err != nil {
			return err
		}
	}
	for _, path := range cleanup {
		if err := cleanupIntegration(root, path, head); err != nil {
			return err
		}
	}
	return nil
}

func cleanupIntegration(root, path, checked string) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	source, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(source, filepath.Join(root, ".worktrees")+string(filepath.Separator)) {
		return errors.New("cleanup must name a source worktree beneath this checkout's .worktrees")
	}
	// Require the exact registered worktree, not a subdirectory or another repo.
	for _, arguments := range [][]string{{"rev-parse", "--show-toplevel"}, {"rev-parse", "--path-format=absolute", "--git-common-dir"}} {
		actual, err := integrationGit(source, arguments...)
		if err != nil {
			return err
		}
		want := source
		if len(arguments) == 3 {
			want, err = integrationGit(root, arguments...)
			if err != nil {
				return err
			}
		}
		if actual != want {
			return errors.New("cleanup target is not this repository's exact source worktree root")
		}
	}
	current, err := integrationGit(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if current != checked {
		return errors.New("HEAD changed during status updates; no cleanup performed")
	}
	branch, err := integrationGit(source, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	// git cherry omits merges, including merge-only conflict resolutions.
	for _, arguments := range [][]string{{"status", "--porcelain", "--untracked-files=all", "--ignored=matching"}, {"cherry", checked, "HEAD"}, {"rev-list", "--merges", checked + "..HEAD"}} {
		output, err := integrationGit(source, arguments...)
		if err != nil {
			return err
		}
		if arguments[0] == "cherry" && !strings.HasPrefix(output, "+") && !strings.Contains(output, "\n+") {
			continue
		}
		if output != "" {
			return fmt.Errorf("source %s has dirty/ignored files or unintegrated patches/merges; retained", source)
		}
	}
	if err := integrationCommand(root, "git", "worktree", "remove", "--", source); err != nil {
		return err
	}
	return integrationCommand(root, "git", "branch", "-D", "--", branch)
}

func integrationGit(directory string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir, command.Stderr = directory, os.Stderr
	data, err := command.Output()
	return strings.TrimSpace(string(data)), err
}

func integrationCommand(directory, name string, args ...string) error {
	fmt.Fprintf(os.Stderr, "integration: %s %s\n", name, strings.Join(args, " "))
	command := exec.Command(name, args...)
	command.Dir = directory
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "integration stopped; inspect retained state before retrying")
		return err
	}
	return nil
}
