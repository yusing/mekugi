package executionobservation

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/activitydetail"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"

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
	target := filepath.Join(f.workspace, "shared.txt")
	writeTestFile(t, target, "one\n")
	observe := func(ref string) mekugiHistory {
		tree := f.store.snapshots.Checkpoint(f.ctx, f.workspace)
		if tree == "" {
			t.Fatal("no workspace checkpoint")
		}
		proxy.execWindows.open(&execWindow{ref: ref, roots: []string{f.workspace}})
		return mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread,
			ExecObservation: &changecapture.Observation{Commands: []commandanalysis.Command{{Command: ref}}, Class: execscope.Opaque.String(), Roots: []string{f.workspace}, Tree: tree}}
	}
	finalize := func(ref string, history mekugiHistory) mekugiHistory {
		if err := finalizeExecObservations(proxy.retained, proxy.changeSource, proxy.snapshots, proxy.recovered, proxy.publishChanges, proxy.execWindows, proxy.activity, proxy.noticeSink, f.ctx, f.workspace, []execCompletion{{callID: ref, history: history, result: binding.DecodeExecutionResult(history.ToolName, responses.MustMarshalJSON(nativeExecOutput("Process exited with code 0")))}}); err != nil {
			t.Fatal(err)
		}
		record, _, err := f.store.lookup(f.ctx, f.workspace, legacycall.ExecCallID(ref, false))
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	long := observe("long")
	short := observe("short")
	writeTestFile(t, target, "two\n")
	first := finalize("short", short)
	writeTestFile(t, target, "three\n")
	second := finalize("long", long)
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
		proxy.execWindows.open(&execWindow{ref: ref, roots: []string{workspace}})
		return mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread,
			ExecObservation: &changecapture.Observation{Commands: []commandanalysis.Command{{Command: ref}}, Class: execscope.Opaque.String(), Roots: []string{workspace}, Tree: tree}}
	}
	outer, nested := observe("outer", f.workspace), observe("nested", inner)
	writeTestFile(t, filepath.Join(f.workspace, "README"), "two\n")
	for _, call := range []struct {
		ref, workspace string
		history        mekugiHistory
	}{{"outer", f.workspace, outer}, {"nested", inner, nested}} {
		if err := finalizeExecObservations(proxy.retained, proxy.changeSource, proxy.snapshots, proxy.recovered, proxy.publishChanges, proxy.execWindows, proxy.activity, proxy.noticeSink, f.ctx, call.workspace, []execCompletion{{callID: call.ref, history: call.history, result: binding.DecodeExecutionResult(call.history.ToolName, responses.MustMarshalJSON(nativeExecOutput("Process exited with code 0")))}}); err != nil {
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
			details := data.ActivityDetail(activitydetail.Request{Operation: activitydetail.Operation{Thread: history.ExecutingThread, Item: "vcs"}})
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
