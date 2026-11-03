// Package environment acquires optional, bounded data about an execution workspace.
package environment

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const Capability = "MEKUGI_ENVIRONMENT_FACTS"

type Snapshot struct {
	ObservedAt time.Time
	Platform   string
	OSName     string
	OSRelease  string
	UserHome   string
	Git        Git
	Incomplete []string
}

type Git struct {
	State         string
	Worktree      string
	Linked        bool
	Branch        string
	Head          string
	DefaultBranch string
	HeadRemotes   []string
	HooksPath     string
	Status        []string
	Recent        []Commit
}

type Commit struct {
	ID      string
	Subject string
}

// Acquire neither fetches nor mutates the repository. All subprocess output and
// acquisition time are bounded; unavailable facts remain explicit data.
func Acquire(ctx context.Context, workspace string) Snapshot {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s := Snapshot{ObservedAt: time.Now().UTC(), Platform: runtime.GOOS, OSName: "unavailable", OSRelease: "unavailable", Git: Git{State: "unavailable", DefaultBranch: "unavailable"}}
	if userHome, err := os.UserHomeDir(); err == nil {
		s.UserHome = userHome
	} else {
		s.Incomplete = append(s.Incomplete, "user home unavailable")
	}
	if !filepath.IsAbs(workspace) {
		s.Incomplete = append(s.Incomplete, "execution workspace unavailable")
		return s
	}
	s.OSName = operatingSystem(ctx, workspace)
	if release, _, err := command(ctx, workspace, "uname", "-sr"); err == nil {
		s.OSRelease = strings.TrimSpace(release)
	} else {
		s.Incomplete = append(s.Incomplete, "os_release unavailable")
	}
	git := func(field string, args ...string) (string, bool) {
		args = append([]string{"--no-lazy-fetch", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "log.showSignature=false"}, args...)
		value, truncated, err := command(ctx, workspace, "git", args...)
		if err != nil {
			// Only Git's discovery failure means absence. Missing executables,
			// corrupt metadata and other inspection failures remain unavailable.
			if exit, ok := err.(*exec.ExitError); ok && field == "repository" && strings.HasPrefix(string(exit.Stderr), "fatal: not a git repository (or any") {
				s.Git.State = "none"
				return "", false
			}
			s.Incomplete = append(s.Incomplete, field+" unavailable")
			return "", false
		}
		if truncated {
			s.Incomplete = append(s.Incomplete, field+" truncated")
		}
		return strings.TrimSuffix(value, "\n"), true
	}
	root, ok := git("repository", "rev-parse", "--show-toplevel")
	if !ok {
		return s
	}
	s.Git.State, s.Git.Worktree = "repository", root
	dir, dirOK := git("git_directory", "rev-parse", "--absolute-git-dir")
	common, commonOK := git("git_common_directory", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if dirOK && commonOK {
		s.Git.Linked = filepath.Clean(dir) != filepath.Clean(common)
	}
	branch, branchOK := git("branch", "symbolic-ref", "--quiet", "--short", "HEAD")
	head, headOK := git("head", "rev-parse", "--verify", "--short=9", "HEAD")
	s.Git.Branch, s.Git.Head = branch, head
	if !branchOK && headOK {
		s.Git.Branch = "detached"
		s.Incomplete = slices.DeleteFunc(s.Incomplete, func(v string) bool { return v == "branch unavailable" })
	}
	if branchOK && !headOK {
		s.Git.Head = "unborn"
		s.Incomplete = slices.DeleteFunc(s.Incomplete, func(v string) bool { return v == "head unavailable" })
	}
	if refs, ok := git("default_branch", "for-each-ref", "--format=%(refname)%00%(symref)", "refs/remotes"); ok {
		var branches []string
		for row := range strings.SplitSeq(refs, "\n") {
			ref, target, _ := strings.Cut(row, "\x00")
			if !strings.HasSuffix(ref, "/HEAD") || !strings.HasPrefix(target, "refs/remotes/") {
				continue
			}
			branch, found := strings.CutPrefix(target, strings.TrimSuffix(ref, "HEAD"))
			if found && branch != "" && !slices.Contains(branches, branch) {
				branches = append(branches, branch)
			}
		}
		if len(branches) == 1 {
			s.Git.DefaultBranch = branches[0]
		}
	}
	if status, ok := git("status", "status", "--porcelain=v1", "--untracked-files=normal", "--ignore-submodules=none"); ok && status != "" {
		for row := range strings.SplitSeq(status, "\n") {
			if len(s.Git.Status) == 32 {
				s.Incomplete = append(s.Incomplete, "status truncated")
				break
			}
			if len(row) > 512 {
				row = validPrefix(row, 512)
				s.Incomplete = append(s.Incomplete, "status entry truncated")
			}
			s.Git.Status = append(s.Git.Status, row)
		}
	}
	if headOK {
		if refs, ok := git("head_remotes", "for-each-ref", "--contains=HEAD", "--format=%(refname:strip=2)%00%(symref)", "refs/remotes"); ok {
			for row := range strings.SplitSeq(refs, "\n") {
				ref, target, found := strings.Cut(row, "\x00")
				if !found || ref == "" || target != "" {
					continue
				}
				if len(s.Git.HeadRemotes) == 16 {
					s.Incomplete = append(s.Incomplete, "head_remotes truncated")
					break
				}
				if len(ref) > 512 {
					ref = validPrefix(ref, 512)
					s.Incomplete = append(s.Incomplete, "head_remotes entry truncated")
				}
				s.Git.HeadRemotes = append(s.Git.HeadRemotes, ref)
			}
		}
		if log, ok := git("recent_commits", "log", "-5", "--abbrev=9", "--format=%h%x00%s%x00", "--no-decorate"); ok {
			for row := range strings.SplitSeq(log, "\x00\n") {
				id, subject, found := strings.Cut(strings.TrimSuffix(row, "\x00"), "\x00")
				if !found {
					continue
				}
				if len(subject) > 256 {
					subject = validPrefix(subject, 256)
					s.Incomplete = append(s.Incomplete, "commit subject truncated")
				}
				s.Git.Recent = append(s.Git.Recent, Commit{ID: id, Subject: subject})
			}
		}
	}
	value, truncated, err := commandWithConfig(ctx, workspace, true, "git", "--no-lazy-fetch", "--no-optional-locks", "config", "--get", "core.hooksPath")
	switch {
	case err == nil:
		value = strings.TrimSuffix(value, "\n")
		if len(value) > 512 {
			value, truncated = validPrefix(value, 512), true
		}
		s.Git.HooksPath = strconv.Quote(value)
		if truncated {
			s.Incomplete = append(s.Incomplete, "hooks_path truncated")
		}
	case isUnsetConfig(err):
		s.Git.HooksPath = "unset (Git default)"
	default:
		s.Git.HooksPath = "unavailable"
		s.Incomplete = append(s.Incomplete, "hooks_path unavailable")
	}
	return s
}

func isUnsetConfig(err error) bool {
	exit, ok := err.(*exec.ExitError)
	return ok && exit.ExitCode() == 1 && len(exit.Stderr) == 0
}

func validPrefix(value string, limit int) string {
	value = value[:min(len(value), limit)]
	for !utf8.ValidString(value) && len(value) != 0 {
		value = value[:len(value)-1]
	}
	return value
}

// A draining bounded writer prevents large repositories from growing retained
// memory. The shared deadline bounds work even when a subprocess keeps writing.
type boundedOutput struct {
	data      []byte
	truncated bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	n := min(len(p), 16384-len(w.data))
	w.data = append(w.data, p[:n]...)
	w.truncated = w.truncated || n != len(p)
	return len(p), nil
}

func command(ctx context.Context, workspace, name string, args ...string) (string, bool, error) {
	return commandWithConfig(ctx, workspace, false, name, args...)
}

// Only the narrow hooksPath read uses effective configuration; other facts stay
// independent of account configuration. Repository-routing variables stay out.
func commandWithConfig(ctx context.Context, workspace string, effectiveConfig bool, name string, args ...string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workspace
	cmd.Env = slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		if effectiveConfig && (key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_SYSTEM" || key == "GIT_CONFIG_NOSYSTEM" || key == "GIT_CONFIG_COUNT" || key == "GIT_CONFIG_PARAMETERS" || strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_")) {
			return false
		}
		return strings.HasPrefix(key, "GIT_")
	})
	cmd.Env = append(cmd.Env, "LC_ALL=C", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if !effectiveConfig {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	}
	var output, stderr boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		exit.Stderr = stderr.data
	}
	return strings.ToValidUTF8(string(output.data), "�"), output.truncated, err
}

// Text supplements native cwd context without repeating an identical worktree path.
func (s Snapshot) Text(workspace string) string {
	var out strings.Builder
	membership := "unavailable"
	switch s.Git.State {
	case "repository":
		membership = "yes"
	case "none":
		membership = "no"
	}
	userHome := "unavailable"
	if s.UserHome != "" {
		userHome = strconv.Quote(s.UserHome)
	}
	fmt.Fprintf(&out, "Observed at: %s\nPlatform: %s\nOS: %s\nKernel: %s\nHome: %s\nGit repository: %s\n", s.ObservedAt.Format(time.RFC3339Nano), s.Platform, s.OSName, s.OSRelease, userHome, membership)
	if s.Git.State == "repository" {
		if s.Git.Worktree != workspace {
			fmt.Fprintf(&out, "Git worktree: %s\n", strconv.Quote(s.Git.Worktree))
		}
		if s.Git.Linked {
			out.WriteString("Linked worktree: true\n")
		}
		fmt.Fprintf(&out, "Branch: %s\nHEAD: %s\nDefault branch: %s\n", s.Git.Branch, s.Git.Head, s.Git.DefaultBranch)
		if s.Git.Head != "unborn" && s.Git.Head != "" {
			remotes := "none"
			if slices.Contains(s.Incomplete, "head_remotes unavailable") {
				remotes = "unavailable"
			} else if len(s.Git.HeadRemotes) != 0 {
				remotes = strings.Join(s.Git.HeadRemotes, ", ")
			}
			fmt.Fprintf(&out, "HEAD in local remote-tracking branches: %s\n", remotes)
		}
		fmt.Fprintf(&out, "Git hooks path: %s\nStatus:\n", s.Git.HooksPath)
		if len(s.Git.Status) == 0 && !slices.Contains(s.Incomplete, "status unavailable") {
			out.WriteString("clean\n")
		}
		for _, row := range s.Git.Status {
			fmt.Fprintln(&out, row)
		}
		if len(s.Git.Recent) != 0 {
			out.WriteString("Recent commits:\n")
			for _, commit := range s.Git.Recent {
				fmt.Fprintf(&out, "%s %s\n", commit.ID, commit.Subject)
			}
		}
	}
	if len(s.Incomplete) != 0 {
		fmt.Fprintf(&out, "Incomplete: %s\n", strings.Join(s.Incomplete, "; "))
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func operatingSystem(ctx context.Context, workspace string) string {
	switch runtime.GOOS {
	case "linux":
		for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(file, 16384))
			file.Close()
			if err == nil {
				if name := releaseName(string(data)); name != "" {
					return name
				}
			}
		}
	case "darwin":
		if version, _, err := command(ctx, workspace, "sw_vers", "-productVersion"); err == nil {
			return "macOS " + strings.TrimSpace(version)
		}
	}
	return "unavailable"
}

func releaseName(data string) string {
	for line := range strings.SplitSeq(data, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "PRETTY_NAME" {
			continue
		}
		if strings.HasPrefix(value, "\"") {
			name, err := strconv.Unquote(value)
			if err != nil {
				return ""
			}
			return name
		}
		return strings.Trim(value, "'")
	}
	return ""
}
