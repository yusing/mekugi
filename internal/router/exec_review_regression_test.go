package router

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestExecReconcileDoesNotClaimUnvisitedListedEntriesDeleted(t *testing.T) {
	t.Parallel()
	cp := execProducerCommand(t, "cp")
	bash := execProducerCommand(t, "bash")
	root := t.TempDir()
	for index := range 600 {
		path := filepath.Join(root, "src", fmt.Sprintf("file-%03d.txt", index))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("source %03d\n", index)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	zPath := filepath.Join(root, "dst", "src", "z")
	writeTestFile(t, zPath, "survives\n")

	command := cp + " -r src dst"
	observation := execProducerCapture(t, root, command)
	execProducerRun(t, bash, root, command)
	if got, err := os.ReadFile(zPath); err != nil || string(got) != "survives\n" {
		t.Fatalf("cp did not preserve dst/src/z: got %q, %v", got, err)
	}

	reviews, _, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	walkTruncated := false
	for _, review := range reviews {
		walkTruncated = walkTruncated || review.Incomplete == "more new files were not read"
		if review.BeforePath == zPath && review.AfterPath == "" {
			t.Fatalf("reconciliation falsely claims surviving dst/src/z was deleted: %+v", review)
		}
	}
	if !walkTruncated {
		t.Fatalf("reconciliation did not exercise the truncated destination walk: %+v", reviews)
	}
}

func TestExecBackupStarRetainsOperandDirectories(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src/a.txt"), "old\n")
	if err := os.MkdirAll(filepath.Join(root, "backup/src"), 0700); err != nil {
		t.Fatal(err)
	}
	command := "sed -i'backup/*' 's/old/new/' src/*.txt"
	execProducerCommand(t, "sed")
	observation := execProducerCapture(t, root, command)
	execProducerRun(t, execProducerCommand(t, "bash"), root, command)
	reviews, complete, coverage, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact {
		t.Fatalf("backup coverage: %v %s", complete, coverage)
	}
	if !slices.ContainsFunc(reviews, func(file mekugi.ReviewFile) bool { return file.AfterPath == filepath.Join(root, "backup/src/a.txt") }) {
		t.Fatalf("backup absent: %+v", reviews)
	}
}

func TestExecInPlaceBackupGlobReconcilesExactBackupReview(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, binary, arguments string
	}{
		{name: "sed", binary: "sed", arguments: "-i.bak 's/old/new/' *.txt"},
		{name: "perl", binary: "perl", arguments: "-i.bak -pe 's/old/new/' *.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			writer := execProducerCommand(t, tc.binary)
			bash := execProducerCommand(t, "bash")
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "a.txt"), "old\n")

			command := writer + " " + tc.arguments
			observation := execProducerCapture(t, root, command)
			execProducerRun(t, bash, root, command)
			for path, want := range map[string]string{
				"a.txt":     "new\n",
				"a.txt.bak": "old\n",
			} {
				if got, err := os.ReadFile(filepath.Join(root, path)); err != nil || string(got) != want {
					t.Fatalf("host result %s = %q, %v; want %q", path, got, err, want)
				}
			}

			reviews, complete, coverage, unswept := reconcileExecObservation(*observation, execReconcileEnv{})
			if !complete || coverage != execCoverageExact || unswept != "" {
				t.Fatalf("reconciliation complete=%v coverage=%q unswept=%q; want exact evidence (reviews=%+v)", complete, coverage, unswept, reviews)
			}
			got := make(map[string]struct {
				fileTitle, diff, incomplete, origin string
			})
			for _, review := range reviews {
				path := review.AfterPath
				if path == "" {
					path = review.BeforePath
				}
				relative, err := filepath.Rel(root, path)
				if err != nil {
					t.Fatalf("review path %q outside root %q: %v", path, root, err)
				}
				got[filepath.ToSlash(relative)] = struct {
					fileTitle, diff, incomplete, origin string
				}{review.Action().Title(), review.Diff, review.Incomplete, review.Origin}
			}
			for relative, want := range map[string]struct {
				title, removed, added string
			}{
				"a.txt":     {title: "Edit", removed: "-old", added: "+new"},
				"a.txt.bak": {title: "Create", added: "+old"},
			} {
				review, ok := got[relative]
				if !ok || review.fileTitle != want.title || review.incomplete != "" || review.origin != "" ||
					!strings.Contains(review.diff, want.added) || want.removed != "" && !strings.Contains(review.diff, want.removed) {
					t.Errorf("review %s = %+v, want complete %s with %q %q", relative, review, want.title, want.removed, want.added)
				}
			}
			gotPaths := make([]string, 0, len(got))
			for path := range got {
				gotPaths = append(gotPaths, path)
			}
			slices.Sort(gotPaths)
			if !slices.Equal(gotPaths, []string{"a.txt", "a.txt.bak"}) {
				t.Errorf("review paths = %q, want [a.txt a.txt.bak]", gotPaths)
			}
		})
	}
}

func TestExecLnSfnCapturesReplacedSymlinkNotChild(t *testing.T) {
	t.Parallel()
	ln := execProducerCommand(t, "ln")
	bash := execProducerCommand(t, "bash")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "targetdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink("targetdir", link); err != nil {
		t.Fatal(err)
	}
	command := ln + " -sfn new link"
	observation := execProducerCapture(t, root, command)
	execProducerRun(t, bash, root, command)
	if target, err := os.Readlink(link); err != nil || target != "new" {
		t.Fatalf("host result link target = %q, %v; want replacement link to new", target, err)
	}
	if info, err := os.Stat(filepath.Join(root, "targetdir")); err != nil || !info.IsDir() {
		t.Fatalf("target directory changed: info=%v err=%v", info, err)
	}

	reviews, complete, coverage, unswept := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || unswept != "" {
		t.Fatalf("reconciliation complete=%v coverage=%q unswept=%q reviews=%+v", complete, coverage, unswept, reviews)
	}
	if len(reviews) != 1 {
		t.Fatalf("reviews = %+v, want only the replaced symlink", reviews)
	}
	review := reviews[0]
	if review.BeforePath != link || review.AfterPath != link || review.Action().Title() != "Edit" || review.Incomplete != "" ||
		!strings.Contains(review.Diff, "targetdir") || !strings.Contains(review.Diff, "new") {
		t.Fatalf("review = %+v, want an exact edit of link from targetdir to new", review)
	}
	if child := filepath.Join(link, "new"); slices.Contains(observation.scopePaths(), child) {
		t.Errorf("pre-call scope captured phantom child %q instead of the destination link", child)
	}
}

func TestNativeExecResultFinalizesAfterReplayProxyReconstruction(t *testing.T) {
	storeDirectory := t.TempDir()
	store, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	firstProxy := newManagedMekugiProxy(t)
	firstProxy.replayStore = store
	workspace := t.TempDir()
	target := filepath.Join(workspace, "a.txt")
	writeTestFile(t, target, "gone\n")
	transform := prepareNativeStockTransform(t, firstProxy, workspace, "exec-before-terminal")
	command := "printf 'run\\n' >> host-run-count && rm a.txt"
	arguments := string(mustMarshalJSON(map[string]any{"cmd": command, "workdir": workspace, "yield_time_ms": 1000}))
	streamNativeExecCommand(t, transform, "exec-call", arguments)
	if history, found, err := store.lookup(t.Context(), workspace, "exec-call"); err != nil || !found || history.ExecObservation == nil {
		t.Fatalf("native call was not durably captured before terminal result: history=%+v found=%v err=%v", history, found, err)
	}
	transform.Close()
	if err := firstProxy.Close(); err != nil {
		t.Fatal(err)
	}

	// Codex owns execution. Simulate its one host execution between the
	// request that captured the call and the later terminal tool result.
	bash := execProducerCommand(t, "bash")
	execProducerRun(t, bash, workspace, command)

	// A new router process has only durable history and the visible terminal
	// result; it must finalize the earlier pre-call snapshot without rerunning
	// the command.
	freshProxy := newManagedMekugiProxy(t)
	t.Cleanup(func() { _ = freshProxy.Close() })
	freshStore, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	freshProxy.replayStore = freshStore
	items := []any{
		map[string]any{"type": "function_call", "call_id": "exec-call", "name": nativeExecCommandToolName, "arguments": arguments},
		map[string]any{"type": "function_call_output", "call_id": "exec-call", "output": nativeExecOutput("Process exited with code 0")},
	}
	transform = reconcileExecItems(t, freshProxy, workspace, items)
	history, found, err := freshStore.lookup(transform.ctx, workspace, "exec-call:exec:1")
	if err != nil || !found || history.ChangeID == "" || history.ExecOutcome == nil ||
		history.ExecOutcome.Status != execStatusCompleted || history.ExecOutcome.Coverage != execCoverageExact {
		t.Fatalf("fresh proxy did not finalize exact captured effects: history=%+v found=%v err=%v", history, found, err)
	}
	got := make(map[string]struct {
		title, diff, incomplete string
	})
	for _, review := range history.ReviewFiles {
		path := review.AfterPath
		if path == "" {
			path = review.BeforePath
		}
		relative, err := filepath.Rel(workspace, path)
		if err != nil {
			t.Fatalf("review path %q outside workspace %q: %v", path, workspace, err)
		}
		got[filepath.ToSlash(relative)] = struct {
			title, diff, incomplete string
		}{review.Action().Title(), review.Diff, review.Incomplete}
	}
	if len(got) != 2 || got["a.txt"].title != "Delete" || !strings.Contains(got["a.txt"].diff, "-gone") ||
		got["a.txt"].incomplete != "" || got["host-run-count"].title != "Create" ||
		!strings.Contains(got["host-run-count"].diff, "+run") || got["host-run-count"].incomplete != "" {
		t.Fatalf("finalized reviews = %+v, want exact delete and once-written marker", history.ReviewFiles)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "host-run-count")); err != nil || string(got) != "run\n" {
		t.Fatalf("host command was rerun during finalization: count=%q err=%v", got, err)
	}

	// Replaying the same terminal input on the reconstructed proxy is
	// idempotent and must not repeat the host command or allocate a new ID.
	reconcileExecItems(t, freshProxy, workspace, items)
	again, found, err := freshStore.lookup(t.Context(), workspace, "exec-call:exec:1")
	if err != nil || !found || again.ChangeID != history.ChangeID {
		t.Fatalf("replay changed derived evidence: history=%+v found=%v err=%v", again, found, err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "host-run-count")); err != nil || string(got) != "run\n" {
		t.Fatalf("replay reran host command: count=%q err=%v", got, err)
	}
}
