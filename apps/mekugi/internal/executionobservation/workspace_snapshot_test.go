package executionobservation

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/activitydetail"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func gitTestWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "test"}} {
		command := exec.Command("git", args...)
		command.Dir = workspace
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return workspace
}

func gitTestCommit(t *testing.T, workspace string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "--quiet", "-m", "base"}} {
		command := exec.Command("git", args...)
		command.Dir = workspace
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}

func TestWorkspaceSnapshotRepositoryIgnoresUserGitConfig(t *testing.T) {
	config := filepath.Join(t.TempDir(), "gitconfig")
	writeTestFile(t, config, "[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = /nonexistent/mekugi-test-signing-program\n")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, "file.txt"), "baseline\n")
	gitTestCommit(t, workspace)
	gitTestRun(t, workspace, "commit", "--quiet", "--allow-empty", "-m", "isolated command")
}

// runSnapshotExec streams one native command, applies effect as the host,
// and reconciles its terminal result. It returns the derived record and the
// reconciling request's context.
func runSnapshotExec(t *testing.T, proxy *Preparer, workspace, callID, command string, effect func()) (mekugiHistory, context.Context) {
	t.Helper()
	transform := prepareNativeStockTransform(t, proxy, workspace, "snapshot-"+callID)
	arguments := string(responses.MustMarshalJSON(map[string]any{"cmd": command, "workdir": workspace}))
	retainCommandObservation(t, transform, callID, arguments)
	effect()
	next := reconcileExecItems(t, proxy, workspace, []any{
		map[string]any{"type": "function_call", "call_id": callID, "name": nativeExecCommandToolName, "arguments": arguments},
		map[string]any{"type": "function_call_output", "call_id": callID, "output": nativeExecOutput("Process exited with code 0")},
	})
	history, _, err := testProxyStore(proxy).lookup(t.Context(), workspace, legacycall.ExecCallID(callID, false))
	if err != nil {
		t.Fatal(err)
	}
	return history, next.ctx
}

func reviewPaths(history mekugiHistory) []string {
	var paths []string
	for _, file := range history.ReviewFiles {
		paths = append(paths, file.BeforePath+" -> "+file.AfterPath)
	}
	return paths
}

func TestWorkspaceSnapshotRecordsUnnamedEditsWithinIgnoreRules(t *testing.T) {
	isolateSnapshotGit(t)
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, ".gitignore"), "node_modules/\n")
	writeTestFile(t, filepath.Join(workspace, "main.go"), "package main\n\nfunc old() {}\n")
	writeTestFile(t, filepath.Join(workspace, "moved.txt"), "moved content\n")
	gitTestCommit(t, workspace)
	writeTestFile(t, filepath.Join(workspace, "notes.md"), "untracked before\n")
	index := filepath.Join(workspace, ".git", "index")
	realIndex, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}

	// The parser cannot name these targets; the host still writes them.
	history, ctx := runSnapshotExec(t, proxy, workspace, "dynamic", "python3 tools/edit.py", func() {
		writeTestFile(t, filepath.Join(workspace, "main.go"), "package main\n\nfunc renamed() {}\n")
		writeTestFile(t, filepath.Join(workspace, "notes.md"), "untracked after\n")
		writeTestFile(t, filepath.Join(workspace, "generated", "new.txt"), "created\n")
		writeTestFile(t, filepath.Join(workspace, "node_modules", "dependency", "index.js"), strings.Repeat("dependency\n", 100))
		if err := os.Rename(filepath.Join(workspace, "moved.txt"), filepath.Join(workspace, "renamed.txt")); err != nil {
			t.Fatal(err)
		}
	})
	if history.ChangeID == "" || history.Source != "python3" {
		t.Fatalf("unnamed edits allocated no change: %+v", history)
	}
	paths := strings.Join(reviewPaths(history), "\n")
	for _, want := range []string{
		filepath.Join(workspace, "main.go") + " -> " + filepath.Join(workspace, "main.go"),
		filepath.Join(workspace, "notes.md") + " -> " + filepath.Join(workspace, "notes.md"),
		" -> " + filepath.Join(workspace, "generated", "new.txt"),
		filepath.Join(workspace, "moved.txt") + " -> " + filepath.Join(workspace, "renamed.txt"),
	} {
		if !strings.Contains(paths, want) {
			t.Errorf("review paths missing %q:\n%s", want, paths)
		}
	}
	if strings.Contains(paths, "node_modules") || len(history.ReviewFiles) != 4 {
		t.Fatalf("ignored or extra paths were recorded:\n%s", paths)
	}
	net, err := testProxyStore(proxy).changes.Read(ctx, testProxyStore(proxy).scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{history.ChangeID}, View: "net"})
	if err != nil || !strings.Contains(net, "-func old() {}") || !strings.Contains(net, "+func renamed() {}") {
		t.Fatalf("net review: %q, %v", net, err)
	}
	if after, err := os.ReadFile(index); err != nil || !bytes.Equal(after, realIndex) {
		t.Fatal("snapshotting changed the repository's own index")
	}

	// A later read-only command takes no checkpoint and records nothing.
	transform := prepareNativeStockTransform(t, proxy, workspace, "snapshot-read")
	retainCommandObservation(t, transform, "read", string(responses.MustMarshalJSON(map[string]any{"cmd": "rg renamed", "workdir": workspace})))
	if _, found := transform.local["read"]; found {
		t.Fatal("a neutral reader was observed")
	}
}

func TestWorkspaceSnapshotRetainedIgnoredPathDoesNotBecomeDeletion(t *testing.T) {
	isolateSnapshotGit(t)
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := gitTestWorkspace(t)
	tracked := filepath.Join(workspace, "tracked.log")
	writeTestFile(t, tracked, "before\n")
	gitTestCommit(t, workspace)
	artifact := filepath.Join(workspace, "build", "cache.bin")
	writeTestFile(t, artifact, strings.Repeat("\x00", changecapture.MaxFileBytes+1))

	history, ctx := runSnapshotExec(t, proxy, workspace, "ignore-transition", "python3 edit.py", func() {
		writeTestFile(t, filepath.Join(workspace, ".gitignore"), "/build/\n*.log\n")
		writeTestFile(t, tracked, "after\n")

	})
	if paths := strings.Join(reviewPaths(history), "\n"); strings.Contains(paths, "cache.bin") || !strings.Contains(paths, "tracked.log") || len(history.ReviewFiles) != 2 {
		t.Fatalf("ignore transition review: %s", paths)
	}
	if _, err := testProxyStore(proxy).changes.Read(ctx, testProxyStore(proxy).scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{history.ChangeID}, View: "net"}); err != nil {
		t.Fatalf("ignored artifact made the net diff incomplete: %v", err)
	}
	// Source-named edits retain coverage even when their path is ignored.
	named, _ := runSnapshotExec(t, proxy, workspace, "named-ignored", "printf 'written\\n' > build/note.txt", func() {
		writeTestFile(t, filepath.Join(workspace, "build", "note.txt"), "written\n")
	})
	if len(named.ReviewFiles) != 1 || !strings.HasSuffix(named.ReviewFiles[0].AfterPath, "build/note.txt") {
		t.Fatalf("named ignored edit: %v", reviewPaths(named))
	}
}

func TestWorkspaceSnapshotPlainWorkspaceBoundsNewTrees(t *testing.T) {
	isolateSnapshotGit(t)
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "existing.txt"), "before\n")
	history, _ := runSnapshotExec(t, proxy, workspace, "plain", "./generate", func() {
		writeTestFile(t, filepath.Join(workspace, "existing.txt"), "after\n")
		writeTestFile(t, filepath.Join(workspace, "small", "new.txt"), "new\n")
		for index := range 257 {
			writeTestFile(t, filepath.Join(workspace, "bulk", fmt.Sprintf("%d.js", index)), "installed\n")
			// A burst in an already-known directory is bounded the same way.
			writeTestFile(t, filepath.Join(workspace, fmt.Sprintf("burst-%d.txt", index)), "generated\n")
		}
	})
	paths := strings.Join(reviewPaths(history), "\n")
	if history.ChangeID == "" || len(history.ReviewFiles) != 2 || !strings.Contains(paths, "existing.txt") ||
		!strings.Contains(paths, filepath.Join("small", "new.txt")) || strings.Contains(paths, "bulk") || strings.Contains(paths, "burst") {
		t.Fatalf("plain workspace record:\n%s", paths)
	}
}

func TestWorkspaceSnapshotOverlappingCallsRecordAnEffectOnce(t *testing.T) {
	isolateSnapshotGit(t)
	f := newMChangesSliceFixture(t, "snapshot-overlap")
	proxy := testProxyWithStore(f.store, &Windows{})
	sibling := testProxyWithStore(f.store, proxy.Windows.ForRuntime())
	target := filepath.Join(f.workspace, "shared.txt")
	writeTestFile(t, target, "one\n")
	observe := func(proxy *Preparer, ref string) mekugiHistory {
		tree := f.store.snapshots.Checkpoint(f.ctx, f.workspace)
		if tree == "" {
			t.Fatal("no workspace checkpoint")
		}
		proxy.Windows.open(&execWindow{ref: ref, roots: []string{f.workspace}})
		return mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread,
			ExecObservation: &changecapture.Observation{Commands: []commandanalysis.Command{{Command: ref}}, Class: execscope.Opaque.String(), Roots: []string{f.workspace}, Tree: tree}}
	}
	finalize := func(proxy *Preparer, ref string, history mekugiHistory) mekugiHistory {
		if err := finalizeExecObservations(proxy.Retained, proxy.Changes, proxy.Checkpoints, proxy.Recovery, proxy.PublishChanges, proxy.Windows, proxy.Audience, proxy.Notice, f.ctx, f.workspace, []execCompletion{{callID: ref, history: history, result: binding.DecodeExecutionResult(history.ToolName, responses.MustMarshalJSON(nativeExecOutput("Process exited with code 0")))}}); err != nil {
			t.Fatal(err)
		}
		record, _, err := f.store.lookup(f.ctx, f.workspace, legacycall.ExecCallID(ref, false))
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	long := observe(proxy, "long")
	short := observe(sibling, "short")
	writeTestFile(t, target, "two\n")
	first := finalize(sibling, "short", short)
	writeTestFile(t, target, "three\n")
	second := finalize(proxy, "long", long)
	if len(first.ReviewFiles) != 1 || !strings.Contains(first.ReviewFiles[0].Diff, "-one") || !strings.Contains(first.ReviewFiles[0].Diff, "+two") {
		t.Fatalf("first record: %+v", first.ReviewFiles)
	}
	if len(second.ReviewFiles) != 1 || !strings.Contains(second.ReviewFiles[0].Diff, "-two") || !strings.Contains(second.ReviewFiles[0].Diff, "+three") {
		t.Fatalf("overlapping record repeated the earlier effect: %+v", second.ReviewFiles)
	}
	net, err := f.store.changes.Read(f.ctx, f.store.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: []string{first.ChangeID, second.ChangeID}, View: "net"})
	if err != nil || !strings.Contains(net, "-one") || !strings.Contains(net, "+three") {
		t.Fatalf("overlapping records do not compose: %q, %v", net, err)
	}
}

func TestWorkspaceSnapshotConcurrentRuntimesRecordAnEffectOnce(t *testing.T) {
	isolateSnapshotGit(t)
	f := newMChangesSliceFixture(t, "snapshot-runtime-overlap")
	parent := testProxyWithStore(f.store, &Windows{})
	child := testProxyWithStore(f.store, parent.Windows.ForRuntime())
	var err error
	child.Checkpoints, err = testCapture(t, f.store.directory).Checkpoints(f.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.workspace, "unnamed.txt")
	writeTestFile(t, target, "before\n")
	proxies := []*Preparer{parent, child}
	refs := []string{"parent", "child"}
	histories := make([]mekugiHistory, len(proxies))
	for i, proxy := range proxies {
		tree := proxy.Checkpoints.Checkpoint(f.ctx, f.workspace)
		if tree == "" {
			t.Fatal("no workspace checkpoint")
		}
		proxy.Windows.open(&execWindow{ref: refs[i], roots: []string{f.workspace}})
		histories[i] = mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread,
			ExecObservation: &changecapture.Observation{Commands: []commandanalysis.Command{{Command: refs[i]}}, Class: execscope.Opaque.String(), Roots: []string{f.workspace}, Tree: tree}}
	}
	writeTestFile(t, target, "after\n")
	// Admit both finalizers before releasing their shared comparison boundary.
	// Each uses its own checkpoint owner, as independently launched runtimes do.
	var workers sync.WaitGroup
	defer workers.Wait()
	release := sync.OnceFunc(parent.Windows.serialize(f.workspace))
	defer release()
	for i, proxy := range proxies {
		workers.Go(func() {
			if err := finalizeExecObservations(proxy.Retained, proxy.Changes, proxy.Checkpoints, proxy.Recovery, proxy.PublishChanges, proxy.Windows, proxy.Audience, proxy.Notice, f.ctx, f.workspace,
				[]execCompletion{{callID: refs[i], history: histories[i], result: binding.ExecutionResult{Terminal: true, Succeeded: true}}}); err != nil {
				t.Error(err)
			}
		})
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		state := parent.Windows.state()
		state.mu.Lock()
		waiting := state.finalizing[f.workspace].users == len(proxies)+1
		state.mu.Unlock()
		if waiting {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("runtime finalizers did not share the workspace comparison boundary")
		case <-time.After(time.Millisecond):
		}
	}
	release()
	workers.Wait()
	effects := 0
	for _, ref := range refs {
		record, _, err := f.store.lookup(f.ctx, f.workspace, legacycall.ExecCallID(ref, false))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range record.ReviewFiles {
			if strings.Contains(file.Diff, "+after") {
				effects++
			}
		}
	}
	if effects != 1 {
		t.Fatalf("one physical edit captured %d times", effects)
	}
	if len(parent.Windows.state().finalizing) != 0 {
		t.Fatal("completed comparisons retained workspace locks")
	}
}

func TestWorkspaceSnapshotExternalScopesSerializeAcrossWorkspaces(t *testing.T) {
	isolateSnapshotGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// The rendezvous shim must not recurse through a PATH-injected VCS guard.
	git := "/usr/bin/git"
	if _, err := os.Stat(git); err != nil {
		t.Skip("requires system Git outside injected PATH")
	}
	gate, bin := t.TempDir(), t.TempDir()
	// Hold concurrent comparisons at diff-tree long enough to expose both
	// windows closing before either claims. A serialized comparison proceeds
	// after the bounded wait without requiring a second comparison to enter.
	shim := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = diff-tree ]; then\n  n=0; : > " + quoteShellWord(gate+"/ready-") + "$$\n  while [ \"$(find " + quoteShellWord(gate) + " -type f | wc -l)\" -lt 2 ] && [ $n -lt 40 ]; do sleep .005; n=$((n+1)); done\n  break\n fi\ndone\nexec " + quoteShellWord(git) + " \"$@\"\n"
	writeTestFile(t, filepath.Join(bin, "git"), shim)
	if err := os.Chmod(filepath.Join(bin, "git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := newMChangesSliceFixture(t, "snapshot-external-concurrency")
	parent := testProxyWithStore(f.store, &Windows{})
	child := testProxyWithStore(f.store, parent.Windows.ForRuntime())
	var err error
	child.Checkpoints, err = testCapture(t, f.store.directory).Checkpoints(f.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	workspaces := []string{f.workspace, t.TempDir()}
	external := t.TempDir()
	target := filepath.Join(external, "shared.txt")
	writeTestFile(t, target, "before\n")
	proxies, refs := []*Preparer{parent, child}, []string{"parent", "child"}
	histories := make([]mekugiHistory, len(proxies))
	for i, proxy := range proxies {
		observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: external, Shell: "bash"}}, false, false, changecapture.Options{Directory: workspaces[i], Workspaces: proxy.Checkpoints})
		if !ok || observation == nil || observation.Checkpoints[external] == "" {
			t.Fatalf("missing explicit-workdir capture: %+v", observation)
		}
		proxy.Windows.open(&execWindow{ref: refs[i], roots: observation.Roots, workspace: workspaces[i]})
		histories[i] = mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread, ExecObservation: observation}
	}
	writeTestFile(t, target, "after\n")
	var workers sync.WaitGroup
	for i, proxy := range proxies {
		workers.Go(func() {
			if err := finalizeExecObservations(proxy.Retained, proxy.Changes, proxy.Checkpoints, proxy.Recovery, proxy.PublishChanges, proxy.Windows, proxy.Audience, proxy.Notice, f.ctx, workspaces[i], []execCompletion{{callID: refs[i], history: histories[i], result: binding.ExecutionResult{Terminal: true, Succeeded: true}}}); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	effects := 0
	for i, ref := range refs {
		record, found, err := f.store.lookup(f.ctx, workspaces[i], legacycall.ExecCallID(ref, false))
		if err != nil || !found {
			t.Fatalf("finalization missing: %v %v", found, err)
		}
		for _, file := range record.ReviewFiles {
			if file.AfterPath == target && strings.Contains(file.Diff, "-before\n+after\n") {
				effects++
			}
		}
	}
	if effects != 1 {
		t.Fatalf("one shared external physical edit captured %d times", effects)
	}
	if len(parent.Windows.state().finalizing) != 0 {
		t.Fatal("completed comparisons retained scope locks")
	}
}

// isolateSnapshotGit keeps the developer's global Git ignore and attribute
// files out of snapshot tests.
func isolateSnapshotGit(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func gitTestRun(t *testing.T, workspace string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = workspace
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil && args[0] != "merge" {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func TestWorkspaceSnapshotRecordsBytesOnDiskDespiteAttributes(t *testing.T) {
	isolateSnapshotGit(t)
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, ".gitattributes"), "*.txt text eol=crlf\n")
	writeTestFile(t, filepath.Join(workspace, "notes.txt"), "a\r\nb\r\nc\r\n")
	gitTestCommit(t, workspace)
	history, _ := runSnapshotExec(t, proxy, workspace, "crlf", "python3 tools/edit.py", func() {
		writeTestFile(t, filepath.Join(workspace, "notes.txt"), "a\r\nB\r\nc\r\n")
	})
	if len(history.ReviewFiles) != 1 {
		t.Fatalf("attribute-converted record: %+v", reviewPaths(history))
	}
	if diff := history.ReviewFiles[0].Diff; strings.Contains(diff, "-a") || !strings.Contains(diff, "-b\r") || !strings.Contains(diff, "+B\r") {
		t.Fatalf("snapshot compared converted content with bytes on disk:\n%s", diff)
	}
}

func TestWorkspaceSnapshotRecoversFromAnUnmergedSeedIndex(t *testing.T) {
	isolateSnapshotGit(t)
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	repository := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(repository, "other", "f"), "base\n")
	writeTestFile(t, filepath.Join(repository, "sub", "x.go"), "before\n")
	gitTestCommit(t, repository)
	gitTestRun(t, repository, "checkout", "--quiet", "-b", "side")
	writeTestFile(t, filepath.Join(repository, "other", "f"), "side\n")
	gitTestRun(t, repository, "commit", "--quiet", "-am", "side")
	gitTestRun(t, repository, "checkout", "--quiet", "-")
	writeTestFile(t, filepath.Join(repository, "other", "f"), "main\n")
	gitTestRun(t, repository, "commit", "--quiet", "-am", "main")
	gitTestRun(t, repository, "merge", "--quiet", "side") // Leaves other/f unmerged.
	workspace := filepath.Join(repository, "sub")
	history, _ := runSnapshotExec(t, proxy, workspace, "unmerged", "python3 edit.py", func() {
		writeTestFile(t, filepath.Join(workspace, "x.go"), "after\n")
	})
	if paths := strings.Join(reviewPaths(history), "\n"); len(history.ReviewFiles) != 1 || !strings.Contains(paths, filepath.Join("sub", "x.go")) {
		t.Fatalf("unmerged seed record:\n%s", paths)
	}
}

func TestStockExecCapturesSubmoduleWriterDirectory(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, missing string
		explicit, dynamic     bool
	}{
		{name: "guarded-cd", prefix: "cd sub && "},
		{name: "unguarded-cd", prefix: "cd sub\n"},
		{name: "explicit-workdir", explicit: true},
		{name: "dynamic-shell-input", prefix: "cd sub && ", dynamic: true},
		{name: "unknown-writer-directory", prefix: `cd "$NESTED" && `,
			missing: "writer working directory is unknown"},
		{name: "substituted-writer-directory", prefix: `cd "$(printf sub)" && `,
			missing: "command substitution"},
		{name: "missed-native-boundary", prefix: "cd sub && ", dynamic: true,
			missing: "native working directory checkpoint unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSnapshotGit(t)
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
			trace := newNativeTraceFixture(t)
			trace.turn = "test-turn"
			transform.sessionShell, transform.shellTurnID = "bash", trace.turn
			thread := transform.shellThreadID
			proxy.NativeTrace = nativetrace.NewWithNotice(trace.root, nil)

			upstream := gitTestWorkspace(t)
			writeTestFile(t, filepath.Join(upstream, "file.txt"), "before\n")
			gitTestCommit(t, upstream)
			gitTestRun(t, workspace, "init", "--quiet")
			gitTestRun(t, workspace, "config", "user.email", "test@example.com")
			gitTestRun(t, workspace, "config", "user.name", "test")
			gitTestRun(t, workspace, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", upstream, "sub")
			gitTestCommit(t, workspace)
			nested := filepath.Join(workspace, "sub")
			workdir := workspace
			if tc.explicit {
				workdir = nested
			}

			command := `cat >"$SESSION_TMP/edit.py" <<'PY'
from pathlib import Path
import subprocess
target = subprocess.check_output(["git", "ls-files"], text=True).strip()
Path(target).write_text("after\n")
subprocess.run(["git", "add", "--", target], check=True)
PY
` + tc.prefix + `python3 "$SESSION_TMP/edit.py"`
			args := string(responses.MustMarshalJSON(map[string]string{"cmd": command, "workdir": workdir}))
			source := "await tools.exec_command(" + args + ");"
			if tc.dynamic {
				source = "const args = () => (" + args + "); await tools.exec_command(args());"
			}
			call := map[string]any{
				"type": "custom_tool_call", "id": "submodule-item", "call_id": "submodule",
				"name": "exec", "input": source, "status": "completed",
			}
			if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"id": "response", "status": "completed", "output": []any{call},
			})); err != nil {
				t.Fatal(err)
			}
			trace.start(thread, "cell", "submodule", source)
			trace.tool(thread, "cell", "shell", nativeExecCommandToolName, args)
			if tc.name != "missed-native-boundary" {
				ctx, cancel := context.WithTimeout(transform.ctx, segmenttransport.BoundaryHold)
				deadline, _ := ctx.Deadline()
				proxy.ObserveNativeBoundary(ctx, segmenttransport.Boundary{
					Thread: thread, Turn: trace.turn, Item: "shell", Tool: "Bash",
					CWD: workdir, Input: command, Deadline: deadline,
				})
				cancel()
			}

			host := exec.Command("bash", "-c", command)
			host.Dir = workdir
			host.Env = append(os.Environ(), "SESSION_TMP="+t.TempDir(), "NESTED="+nested,
				"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
			if output, err := host.CombinedOutput(); err != nil {
				t.Fatalf("shell command: %v: %s", err, output)
			}
			trace.result(thread, "shell", "completed", map[string]any{"exit_code": 0})
			trace.end(thread, "cell")
			request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{
				"input": mustTestJSON(t, []any{call, map[string]any{
					"type": "custom_tool_call_output", "call_id": "submodule",
					"output": "Script completed\nWall time 0.1 seconds\nOutput:\n",
				}}),
			}}
			if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
				t.Fatal(err)
			}
			record, found, err := testProxyStore(proxy).lookup(t.Context(), workspace, "submodule:effects")
			if err != nil || !found || record.ExecOutcome == nil ||
				record.ExecOutcome.Status != changecapture.StatusCompleted {
				t.Fatalf("missing completed effect: outcome=%+v found=%v err=%v", record.ExecOutcome, found, err)
			}
			if tc.missing != "" {
				if record.ExecOutcome.Coverage != changecapture.CoveragePartial ||
					!strings.Contains(record.ExecOutcome.ScopeReason, tc.missing) ||
					len(record.ReviewFiles) != 0 {
					t.Fatalf("unknown baseline: %+v; review paths=%q", record.ExecOutcome, reviewPaths(record))
				}
				return
			}
			target := filepath.Join(nested, "file.txt")
			if record.ExecOutcome.Coverage != changecapture.CoverageExact || record.ChangeID == "" ||
				len(record.ReviewFiles) != 1 || record.ReviewFiles[0].AfterPath != target ||
				!strings.Contains(record.ReviewFiles[0].Diff, "-before\n+after\n") {
				t.Fatalf("submodule edit: %+v; review paths=%q", record.ExecOutcome, reviewPaths(record))
			}
		})
	}
}

func TestWorkspaceSnapshotClaimsStayWithinTheirWorkspace(t *testing.T) {
	isolateSnapshotGit(t)
	f := newMChangesSliceFixture(t, "snapshot-domains")
	gitTestRun(t, f.workspace, "init", "--quiet")
	inner := filepath.Join(f.workspace, "pkg")
	writeTestFile(t, filepath.Join(f.workspace, "README"), "one\n")
	writeTestFile(t, filepath.Join(inner, "code.go"), "package pkg\n")
	proxy := testProxyWithStore(f.store, &Windows{})
	observe := func(ref, workspace string) mekugiHistory {
		tree := f.store.snapshots.Checkpoint(f.ctx, workspace)
		if tree == "" {
			t.Fatal("no workspace checkpoint")
		}
		proxy.Windows.open(&execWindow{ref: ref, roots: []string{workspace}})
		return mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread,
			ExecObservation: &changecapture.Observation{Commands: []commandanalysis.Command{{Command: ref}}, Class: execscope.Opaque.String(), Roots: []string{workspace}, Tree: tree}}
	}
	outer, nested := observe("outer", f.workspace), observe("nested", inner)
	writeTestFile(t, filepath.Join(f.workspace, "README"), "two\n")
	for _, call := range []struct {
		ref, workspace string
		history        mekugiHistory
	}{{"outer", f.workspace, outer}, {"nested", inner, nested}} {
		if err := finalizeExecObservations(proxy.Retained, proxy.Changes, proxy.Checkpoints, proxy.Recovery, proxy.PublishChanges, proxy.Windows, proxy.Audience, proxy.Notice, f.ctx, call.workspace, []execCompletion{{callID: call.ref, history: call.history, result: binding.DecodeExecutionResult(call.history.ToolName, responses.MustMarshalJSON(nativeExecOutput("Process exited with code 0")))}}); err != nil {
			t.Fatal(err)
		}
	}
	outerRecord, _, err := f.store.lookup(f.ctx, f.workspace, legacycall.ExecCallID("outer", false))
	if err != nil || len(outerRecord.ReviewFiles) != 1 {
		t.Fatalf("outer record: %+v, %v", reviewPaths(outerRecord), err)
	}
	nestedRecord, _, err := f.store.lookup(f.ctx, inner, legacycall.ExecCallID("nested", false))
	if err != nil || len(nestedRecord.ReviewFiles) != 0 {
		t.Fatalf("a claim crossed into another workspace's comparison: %+v, %v", reviewPaths(nestedRecord), err)
	}
}

func TestWorkspaceSnapshotVCSChangesStayOutOfAuthoredTotals(t *testing.T) {
	for _, operation := range []string{"cherry-pick", "rebase", "reset --hard", "mixed"} {
		t.Run(operation, func(t *testing.T) {
			mixed := operation == "mixed"
			isolateSnapshotGit(t)
			workspace := gitTestWorkspace(t)
			writeTestFile(t, filepath.Join(workspace, "authored.txt"), "before\n")
			gitTestCommit(t, workspace)
			base := strings.TrimSpace(string(runExecVCSTestGit(t, workspace, "branch", "--show-current")))
			gitTestRun(t, workspace, "switch", "-c", "incoming")
			writeTestFile(t, filepath.Join(workspace, "imported.txt"), "imported\n")
			gitTestCommit(t, workspace)
			gitTestRun(t, workspace, "switch", base)
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			// Global options force snapshot coverage, outside the Git scope provider.
			command := "git -c core.fsmonitor=false " + operation + " incoming"
			if mixed {
				command = "env -u BASH_ENV rtk proxy git switch incoming; python3 -c " + commandargs.QuoteArgument("from pathlib import Path; Path('authored.txt').write_text('after\\n')")
			}
			history, ctx := runSnapshotExec(t, proxy, workspace, "vcs", command, func() {
				if mixed {
					gitTestRun(t, workspace, "switch", "incoming")
					writeTestFile(t, filepath.Join(workspace, "authored.txt"), "after\n")
				} else {
					gitTestRun(t, workspace, append(strings.Fields(operation), "incoming")...)
				}
			})
			if history.ChangeID == "" || len(history.ReviewFiles) != map[bool]int{false: 1, true: 2}[mixed] {
				t.Fatalf("VCS history lost evidence: %+v", history)
			}
			// A new store reads durable provenance, without command re-execution.
			resumed, err := openMekugiReplayStore(testProxyStore(proxy).directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(resumed.snapshots.Close)
			list, err := resumed.changes.Read(ctx, resumed.scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{history.ChangeID}, View: "list"})
			if err != nil {
				t.Fatal(err)
			}
			if mixed {
				if list != history.ChangeID+" +1 -1\n" {
					t.Fatalf("mixed authored counts: %q", list)
				}
			} else if list != "" {
				t.Fatalf("VCS counted as authored: %q", list)
			}
			data, err := resumed.liveDiffSnapshot(ctx, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {history.ExecutingThread: true}}})
			if err != nil || len(data.Publication.Files) != map[bool]int{false: 0, true: 1}[mixed] {
				t.Fatalf("saved Diff: %+v, %v", data, err)
			}
			if receipts := data.Receipts(); len(receipts) != 1 || !strings.Contains(receipts[0].Text, "tool-managed files (diagnostic: +1 -0)") || strings.Contains(receipts[0].Text, "imported.txt") {
				t.Fatalf("Git diagnostic receipt: %+v", receipts)
			}
			details := data.ActivityDetail(activitydetail.Request{Thread: history.ExecutingThread, Item: "vcs"})
			if len(details.Pages) != len(history.ReviewFiles) || !strings.Contains(details.Pages[len(details.Pages)-1].Code, "+imported") {
				t.Fatalf("Git diagnostic details: %+v", details)
			}
			diagnostic, err := resumed.changes.Read(ctx, resumed.scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{history.ChangeID}, View: "history"})
			if err != nil || !strings.Contains(diagnostic, "+imported") {
				t.Fatalf("VCS history missing: %q, %v", diagnostic, err)
			}
		})
	}
}
