package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
)

func inventoryGit(t *testing.T, workspace string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

// inventoryRepository commits files into a new repository at workspace.
func inventoryRepository(t *testing.T, workspace string, files map[string]string) {
	t.Helper()
	inventoryGit(t, workspace, "init", "--quiet")
	for name, content := range files {
		writeTestFile(t, filepath.Join(workspace, name), content)
	}
	inventoryGit(t, workspace, "add", "-A")
	inventoryGit(t, workspace, "commit", "--quiet", "-m", "baseline")
}

func observeInventoryCommand(t *testing.T, workspace, workdir, command string, excluded []string) *execObservation {
	t.Helper()
	observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: workdir, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace, excluded: excluded})
	if !observed || observation == nil || observation.Inventory == nil {
		t.Fatalf("opaque command %q has no workspace inventory: %+v", command, observation)
	}
	return observation
}

func inventoryReviews(t *testing.T, observation *execObservation, excluded []string) []mekugi.ReviewFile {
	t.Helper()
	reviews, complete, coverage, _ := reconcileExecObservation(*observation, execReconcileEnv{excluded: excluded})
	if complete || coverage != execCoveragePartial {
		t.Fatalf("workspace inventory cannot prove all effects: complete=%v coverage=%q", complete, coverage)
	}
	return reviews
}

func inventoryReview(t *testing.T, workspace string, reviews []mekugi.ReviewFile, name string) mekugi.ReviewFile {
	t.Helper()
	index := slices.IndexFunc(reviews, func(review mekugi.ReviewFile) bool {
		return cmpOrPath(review.AfterPath, review.BeforePath) == filepath.Join(workspace, name)
	})
	if index < 0 {
		t.Fatalf("no review for %s in %v", name, reviewSummary(workspace, reviews))
	}
	return reviews[index]
}

func TestExecInventoryOpaqueCommandsGetExactGitDiffs(t *testing.T) {
	for _, command := range []string{"make test", "go test ./...", "unknown-build-tool"} {
		t.Run(command, func(t *testing.T) {
			workspace := t.TempDir()
			inventoryRepository(t, workspace, map[string]string{"existing.txt": "before\n", "deleted.txt": "deleted baseline\n", "dirty.txt": "committed\n"})
			// A modified tracked file and an untracked file have no matching
			// index blob; their content is captured before the call.
			writeTestFile(t, filepath.Join(workspace, "dirty.txt"), "uncommitted\n")
			writeTestFile(t, filepath.Join(workspace, "untracked.txt"), "untracked before\n")
			observation := observeInventoryCommand(t, workspace, workspace, command, nil)
			if inventory := observation.Inventory; len(inventory.Blobs) != 2 || len(inventory.Files) != 2 {
				t.Fatalf("inventory blobs=%v files=%d, want clean files by blob id and dirty files by content", inventory.Blobs, len(inventory.Files))
			}
			writeTestFile(t, filepath.Join(workspace, "existing.txt"), "after\n")
			writeTestFile(t, filepath.Join(workspace, "dirty.txt"), "dirty after\n")
			writeTestFile(t, filepath.Join(workspace, "untracked.txt"), "untracked after\n")
			if err := os.Remove(filepath.Join(workspace, "deleted.txt")); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(workspace, "testdata", "snapshots", "new.txt"), "new snapshot\n")
			reviews := inventoryReviews(t, observation, nil)
			want := []string{"Create testdata/snapshots/new.txt", "Delete deleted.txt", "Edit dirty.txt", "Edit existing.txt", "Edit untracked.txt"}
			if got := reviewSummary(workspace, reviews); !slices.Equal(got, want) {
				t.Fatalf("reviews = %v, want %v", got, want)
			}
			for name, diff := range map[string][]string{
				"existing.txt":               {"-before", "+after"},
				"deleted.txt":                {"-deleted baseline"},
				"dirty.txt":                  {"-uncommitted", "+dirty after"},
				"untracked.txt":              {"-untracked before", "+untracked after"},
				"testdata/snapshots/new.txt": {"+new snapshot"},
			} {
				review := inventoryReview(t, workspace, reviews, name)
				if review.Incomplete != "" || review.OriginNote != execInventoryNote {
					t.Fatalf("%s is not exact window evidence: %+v", name, review)
				}
				for _, row := range diff {
					if !strings.Contains(review.Diff, row) {
						t.Fatalf("%s diff lacks %q: %s", name, row, review.Diff)
					}
				}
			}
		})
	}
}

func TestExecInventoryIdenticalEndpointsProduceNoDiff(t *testing.T) {
	workspace := t.TempDir()
	inventoryRepository(t, workspace, map[string]string{"same.txt": "same\n"})
	observation := observeInventoryCommand(t, workspace, workspace, "make test", nil)
	// Equality is an endpoint observation, not a claim that nothing was written.
	writeTestFile(t, filepath.Join(workspace, "same.txt"), "temporary\n")
	writeTestFile(t, filepath.Join(workspace, "same.txt"), "same\n")
	if reviews := inventoryReviews(t, observation, nil); len(reviews) != 0 {
		t.Fatalf("identical endpoints invented a diff: %+v", reviews)
	}
}

func TestExecInventoryRecordsIgnoredFilesWithoutDependencyNoise(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	inventoryRepository(t, workspace, map[string]string{".gitignore": "FIXME.md\nnode_modules/\n", "source.txt": "before\n"})
	writeTestFile(t, filepath.Join(workspace, "FIXME.md"), "before\n")
	writeTestFile(t, filepath.Join(workspace, "node_modules", "pkg", "file.js"), "dependency before\n")
	writeTestFile(t, filepath.Join(outside, "outside.txt"), "outside\n")
	if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil {
		t.Fatal(err)
	}
	observation := observeInventoryCommand(t, workspace, workspace, "make test", nil)
	for relative := range observation.Inventory.Entries {
		if strings.HasPrefix(relative, "node_modules/") || strings.HasPrefix(relative, ".git/") || strings.HasPrefix(relative, "linked/") {
			t.Fatalf("pruned path inventoried: %s", relative)
		}
	}
	writeTestFile(t, filepath.Join(workspace, "FIXME.md"), "after\n")
	writeTestFile(t, filepath.Join(workspace, "node_modules", "pkg", "file.js"), "dependency after\n")
	writeTestFile(t, filepath.Join(outside, "outside.txt"), "changed outside\n")
	reviews := inventoryReviews(t, observation, nil)
	if got := reviewSummary(workspace, reviews); !slices.Equal(got, []string{"Edit FIXME.md", "Edit node_modules incomplete"}) {
		t.Fatalf("reviews=%v", got)
	}
	review := inventoryReview(t, workspace, reviews, "FIXME.md")
	if review.Incomplete != "" || !strings.Contains(review.Diff, "-before") || !strings.Contains(review.Diff, "+after") {
		t.Fatalf("ignored file baseline lost: %+v", review)
	}
	directory := inventoryReview(t, workspace, reviews, "node_modules")
	if !directory.Directory || directory.Diff != "" {
		t.Fatalf("dependency content retained: %+v", directory)
	}
}

func TestExecInventoryUsesSelectedMetadataDirectory(t *testing.T) {
	workspace, commandDirectory := t.TempDir(), t.TempDir()
	inventoryRepository(t, workspace, map[string]string{"selected.txt": "before\n"})
	writeTestFile(t, filepath.Join(commandDirectory, "unselected.txt"), "before\n")
	observation := observeInventoryCommand(t, workspace, commandDirectory, "unknown-build-tool", nil)
	writeTestFile(t, filepath.Join(workspace, "selected.txt"), "after\n")
	writeTestFile(t, filepath.Join(commandDirectory, "unselected.txt"), "after\n")
	if got := reviewSummary(workspace, inventoryReviews(t, observation, nil)); !slices.Equal(got, []string{"Edit selected.txt"}) {
		t.Fatalf("inventory followed the command directory instead of the selected workspace: %v", got)
	}
}

func TestExecInventoryExcludesSameCellPatchesAndOverlappingScope(t *testing.T) {
	workspace := t.TempDir()
	inventoryRepository(t, workspace, map[string]string{"patched.txt": "patch before\n", "generated.txt": "before\n", "shared/claimed.txt": "before\n"})
	patched := filepath.Join(workspace, "patched.txt")
	observation := observeInventoryCommand(t, workspace, workspace, "make test", []string{patched})
	writeTestFile(t, patched, "patch after\n")
	writeTestFile(t, filepath.Join(workspace, "generated.txt"), "after\n")
	writeTestFile(t, filepath.Join(workspace, "shared", "claimed.txt"), "after\n")
	writeTestFile(t, filepath.Join(workspace, "shared", "created.txt"), "new\n")
	got := reviewSummary(workspace, inventoryReviews(t, observation, []string{filepath.Join(workspace, "shared")}))
	if !slices.Equal(got, []string{"Edit generated.txt"}) {
		t.Fatalf("same-cell patch or overlapping writer scope attributed again: %v", got)
	}
}

func TestExecInventoryPreservesDeclaredPathPriority(t *testing.T) {
	workspace := t.TempDir()
	inventoryRepository(t, workspace, map[string]string{"tracked.txt": "tracked\n"})
	// An untracked file whose content the inventory would read before the
	// declared write consumes the whole budget if explicit scope comes second.
	writeTestFile(t, filepath.Join(workspace, "a-large.txt"), strings.Repeat("x", maxExecContentBytes))
	target := filepath.Join(workspace, "z-declared.txt")
	writeTestFile(t, target, "declared baseline\n")
	observation := observeInventoryCommand(t, workspace, workspace, "printf after > z-declared.txt; make test", nil)
	if !slices.ContainsFunc(observation.Files, func(file execFileSnapshot) bool {
		return file.Path == target && file.Content == "declared baseline\n" && file.Error == ""
	}) {
		t.Fatal("inventory displaced the declared-path baseline")
	}
	writeTestFile(t, target, "after\n")
	review := inventoryReview(t, workspace, inventoryReviews(t, observation, nil), "z-declared.txt")
	if review.Incomplete != "" || review.OriginNote != "" || !strings.Contains(review.Diff, "-declared baseline") || !strings.Contains(review.Diff, "+after") {
		t.Fatalf("declared path lost its exact, attributed diff: %+v", review)
	}
}

func TestExecInventoryWithoutGitRetainsBoundedContent(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "edited.txt"), "before\n")
	writeTestFile(t, filepath.Join(workspace, "deleted.txt"), "before\n")
	writeTestFile(t, filepath.Join(workspace, "unchanged.txt"), "same\n")
	observation := observeInventoryCommand(t, workspace, workspace, "make test", nil)
	if inventory := observation.Inventory; len(inventory.Entries) != 3 || len(inventory.Blobs) != 0 || len(inventory.Files) != 3 {
		t.Fatalf("inventory without git lost content: %+v", inventory)
	}
	writeTestFile(t, filepath.Join(workspace, "edited.txt"), "after, longer\n")
	if err := os.Remove(filepath.Join(workspace, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "created.txt"), "created\n")
	reviews := inventoryReviews(t, observation, nil)
	if got := reviewSummary(workspace, reviews); !slices.Equal(got, []string{"Create created.txt", "Delete deleted.txt", "Edit edited.txt"}) {
		t.Fatalf("reviews = %v", got)
	}
	for _, name := range []string{"edited.txt", "deleted.txt"} {
		review := inventoryReview(t, workspace, reviews, name)
		if review.Incomplete != "" || !strings.Contains(review.Diff, "-before") {
			t.Fatalf("%s without before-content became a diff: %+v", name, review)
		}
	}
	if created := inventoryReview(t, workspace, reviews, "created.txt"); created.Incomplete != "" || !strings.Contains(created.Diff, "+created") {
		t.Fatalf("creation lost its content: %+v", created)
	}
}

func TestExecInventoryRejectsBlobsThatAreNotTheFileContent(t *testing.T) {
	workspace := t.TempDir()
	inventoryGit(t, workspace, "init", "--quiet")
	// The index holds LF; the worktree holds CRLF. Git calls the file clean.
	writeTestFile(t, filepath.Join(workspace, ".gitattributes"), "converted.txt text eol=crlf\n")
	writeTestFile(t, filepath.Join(workspace, "converted.txt"), "one\ntwo\n")
	for _, name := range []string{"assumed.txt", "skipped.txt"} {
		writeTestFile(t, filepath.Join(workspace, name), "index\n")
	}
	inventoryGit(t, workspace, "add", "-A")
	inventoryGit(t, workspace, "commit", "--quiet", "-m", "baseline")
	if err := os.Remove(filepath.Join(workspace, "converted.txt")); err != nil {
		t.Fatal(err)
	}
	inventoryGit(t, workspace, "checkout", "--", "converted.txt")
	inventoryGit(t, workspace, "update-index", "--assume-unchanged", "assumed.txt")
	inventoryGit(t, workspace, "update-index", "--skip-worktree", "skipped.txt")
	// Git reports neither flagged file as modified, yet the worktree differs.
	writeTestFile(t, filepath.Join(workspace, "assumed.txt"), "worktree\n")
	writeTestFile(t, filepath.Join(workspace, "skipped.txt"), "worktree\n")
	observation := observeInventoryCommand(t, workspace, workspace, "make test", nil)
	for _, name := range []string{"assumed.txt", "skipped.txt"} {
		if observation.Inventory.Blobs[name] != "" {
			t.Fatalf("%s is served from the index despite its flag", name)
		}
	}
	if observation.Inventory.Blobs["converted.txt"] != "" {
		t.Fatal("converted worktree bytes cannot use the index blob")
	}
	for _, name := range []string{"converted.txt", "assumed.txt", "skipped.txt"} {
		writeTestFile(t, filepath.Join(workspace, name), "after\n")
	}
	reviews := inventoryReviews(t, observation, nil)
	for _, name := range []string{"converted.txt", "assumed.txt", "skipped.txt"} {
		review := inventoryReview(t, workspace, reviews, name)
		if review.Incomplete != "" || strings.Contains(review.Diff, "-index") {
			t.Fatalf("%s took before-content from a mismatched index blob: %+v", name, review)
		}
	}
}

func TestExecInventoryCodeModeBaselineResolvesFromInventory(t *testing.T) {
	workspace := t.TempDir()
	inventoryRepository(t, workspace, map[string]string{".gitignore": "build/\n", "clean.txt": "clean before\n"})
	writeTestFile(t, filepath.Join(workspace, "untracked.txt"), "untracked before\n")
	writeTestFile(t, filepath.Join(workspace, "build", "out.txt"), "ignored\n")
	source := "const patch = buildPatch(); await tools.apply_patch(patch);"
	commands, dynamic := stockLiteralExecCommands(source, workspace, "bash")
	observation, _ := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: workspace})
	baseline := captureResolvedBaseline(workspace, "bash")
	if observation != nil || baseline.Inventory == nil || len(baseline.Files) != 0 {
		t.Fatalf("patch-only cell must hold one baseline inventory: observation=%+v baseline=%+v", observation, baseline)
	}
	// The cell wrote the files before resolution reads the baseline.
	writeTestFile(t, filepath.Join(workspace, "clean.txt"), "clean after\n")
	writeTestFile(t, filepath.Join(workspace, "untracked.txt"), "untracked after\n")
	for path, want := range map[string]execFileSnapshot{
		"clean.txt":     {Kind: execFileText, Content: "clean before\n"},
		"untracked.txt": {Kind: execFileText, Content: "untracked before\n"},
		"new.txt":       {},
		"build/out.txt": {Kind: execFileText, Content: "ignored\n"},
	} {
		got := baseline.file(filepath.Join(workspace, path))
		if got.Kind != want.Kind || got.Content != want.Content || got.Error != want.Error {
			t.Fatalf("%s baseline = %+v, want %+v", path, got, want)
		}
	}

	// Non-Git edits retain the same bounded pre-cell content coverage.
	plain := t.TempDir()
	writeTestFile(t, filepath.Join(plain, "unchanged.txt"), "unchanged\n")
	writeTestFile(t, filepath.Join(plain, "edited.txt"), "before\n")
	baseline = captureResolvedBaseline(plain, "bash")
	writeTestFile(t, filepath.Join(plain, "edited.txt"), "after, longer\n")
	for path, want := range map[string]execFileSnapshot{
		"unchanged.txt": {Kind: execFileText, Content: "unchanged\n"},
		"edited.txt":    {Kind: execFileText, Content: "before\n"},
	} {
		got := baseline.file(filepath.Join(plain, path))
		if got.Kind != want.Kind || got.Content != want.Content || got.Error != want.Error {
			t.Fatalf("%s baseline without git = %+v, want %+v", path, got, want)
		}
	}
}

func TestExecInventoryStoresNoBlobsForCleanFiles(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	store := proxy.replayStore
	workspace := t.TempDir()
	files := make(map[string]string)
	for i := range 64 {
		files[filepath.Join("pkg", string(rune('a'+i%26))+strings.Repeat("x", i/26), "source.go")] = strings.Repeat("// clean source\n", 128)
	}
	inventoryRepository(t, workspace, files)
	transform := prepareNativeStockTransform(t, proxy, workspace, "inventory-blobs")
	arguments := string(mustMarshalJSON(map[string]any{"cmd": "make test", "workdir": workspace}))
	streamNativeExecCommand(t, transform, "inventory-call", arguments)
	retained, found, err := store.lookup(t.Context(), workspace, "inventory-call")
	if err != nil || !found || retained.ExecObservation == nil || retained.ExecObservation.Inventory == nil {
		t.Fatalf("opaque call lacked a durable inventory: found=%v err=%v", found, err)
	}
	if inventory := retained.ExecObservation.Inventory; len(inventory.Blobs) != 64 || len(inventory.Files) != 0 {
		t.Fatalf("clean files were not recorded by blob id: blobs=%d files=%d", len(inventory.Blobs), len(inventory.Files))
	}
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 0 {
		t.Fatalf("clean files stored snapshot blobs: %v", blobs)
	}
}

// The inventory survives a router restart between the call and its result:
// the fresh router reads before-content from the retained blob ids.
func TestExecInventoryNativeYieldCompletesAfterRestart(t *testing.T) {
	storeDirectory, workspace := t.TempDir(), t.TempDir()
	inventoryRepository(t, workspace, map[string]string{"source.txt": "before\n"})
	store, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	first := newManagedMekugiProxy(t)
	first.replayStore = store
	transform := prepareNativeStockTransform(t, first, workspace, "inventory-before-terminal")
	arguments := string(mustMarshalJSON(map[string]any{"cmd": "make test", "workdir": workspace, "yield_time_ms": 1000}))
	streamNativeExecCommand(t, transform, "inventory-call", arguments)
	// Only the host changes files; observation and replay never run the command.
	writeTestFile(t, filepath.Join(workspace, "source.txt"), "after\n")
	writeTestFile(t, filepath.Join(workspace, "testdata", "snapshots", "new.txt"), "snapshot\n")
	items := []any{
		map[string]any{"type": "function_call", "call_id": "inventory-call", "name": nativeExecCommandToolName, "arguments": arguments},
		map[string]any{"type": "function_call_output", "call_id": "inventory-call", "output": nativeExecOutput("Process running with session ID 9")},
	}
	reconcileExecItems(t, first, workspace, items)
	if _, found, err := store.lookup(t.Context(), workspace, "inventory-call:exec:1"); err != nil || found {
		t.Fatalf("yielded command finalized before terminal completion: found=%v err=%v", found, err)
	}
	transform.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	freshStore, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	fresh := newManagedMekugiProxy(t)
	fresh.replayStore = freshStore
	t.Cleanup(func() { _ = fresh.Close() })
	items = append(items,
		map[string]any{"type": "function_call", "call_id": "inventory-poll", "name": "write_stdin", "arguments": string(mustMarshalJSON(map[string]any{"session_id": 9, "chars": ""}))},
		map[string]any{"type": "function_call_output", "call_id": "inventory-poll", "output": nativeExecOutput("Process exited with code 0")},
	)
	transform = reconcileExecItems(t, fresh, workspace, items)
	history, found, err := freshStore.lookup(t.Context(), workspace, "inventory-call:exec:1")
	if err != nil || !found || history.ChangeID == "" || history.ExecOutcome == nil ||
		history.ExecOutcome.Status != execStatusCompleted || history.ExecOutcome.Coverage != execCoveragePartial {
		t.Fatalf("terminal result lost partial workspace evidence: %+v found=%v err=%v", history, found, err)
	}
	if got := reviewSummary(workspace, history.ReviewFiles); !slices.Equal(got, []string{"Create testdata/snapshots/new.txt", "Edit source.txt"}) {
		t.Fatalf("durable reviews = %v", got)
	}
	changes, err := freshStore.readChanges(transform.ctx, changeReadOptions{workspace: workspace, ids: []string{history.ChangeID}, view: "history", maxTokens: 4000})
	if err != nil || !strings.Contains(changes, "-before") || !strings.Contains(changes, "+after") || !strings.Contains(changes, "+snapshot") {
		t.Fatalf("durable mchanges evidence = %q, err=%v", changes, err)
	}
	// Later writes cannot alter completed evidence or reallocate its ID.
	writeTestFile(t, filepath.Join(workspace, "source.txt"), "later\n")
	reconcileExecItems(t, fresh, workspace, items)
	again, found, err := freshStore.lookup(t.Context(), workspace, "inventory-call:exec:1")
	if err != nil || !found || again.ChangeID != history.ChangeID {
		t.Fatalf("replay reallocated evidence: %+v found=%v err=%v", again, found, err)
	}
	changesAgain, err := freshStore.readChanges(transform.ctx, changeReadOptions{workspace: workspace, ids: []string{history.ChangeID}, view: "history", maxTokens: 4000})
	if err != nil || changesAgain != changes {
		t.Fatalf("replay changed retained evidence: err=%v before=%q after=%q", err, changes, changesAgain)
	}
}

func TestExecInventoryCutListingInventsNoCreations(t *testing.T) {
	collect := func(inventory *execInventory) []string {
		var created []string
		gaps, _ := reconcileExecInventory(inventory, func(string) bool { return false }, func(before, after execFileSnapshot) {
			created = append(created, before.Path)
		}, new(maxExecContentBytes))
		if len(gaps) != 1 || gaps[0].BeforePath != inventory.Root || gaps[0].OriginNote != execInventoryNote {
			t.Fatalf("cut listing lacks its named gap: %+v", gaps)
		}
		return created
	}
	t.Run("deadline", func(t *testing.T) {
		workspace := t.TempDir()
		inventoryRepository(t, workspace, map[string]string{"a.txt": "a\n", "c/d.txt": "d\n"})
		inventory := captureExecInventory(workspace, time.Now().Add(-time.Second), new(maxExecContentBytes))
		if created := collect(inventory); len(created) != 0 {
			t.Fatalf("existing files reported as created: %v", created)
		}
	})
	t.Run("file limit", func(t *testing.T) {
		workspace := t.TempDir()
		for index := range maxExecListingEntries + 100 {
			writeTestFile(t, filepath.Join(workspace, fmt.Sprintf("d%02d", index/100), fmt.Sprintf("f%05d.txt", index)), "x")
		}
		inventory := captureExecInventory(workspace, time.Now().Add(time.Minute), new(maxExecContentBytes))
		if created := collect(inventory); len(created) != 0 {
			t.Fatalf("%d existing files reported as created, first %s", len(created), created[0])
		}
	})
}

func TestExecInventoryHonorsNestedRepositories(t *testing.T) {
	workspace := t.TempDir()
	inventoryRepository(t, workspace, map[string]string{"root.txt": "root\n"})
	nested := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	inventoryRepository(t, nested, map[string]string{".gitignore": "build/\n", "lib.txt": "before\n"})
	writeTestFile(t, filepath.Join(nested, "build", "old.o"), "old")
	// A linked worktree or submodule spells its metadata as a .git file.
	writeTestFile(t, filepath.Join(workspace, "linked", ".git"), "gitdir: /nonexistent\n")
	writeTestFile(t, filepath.Join(workspace, "linked", "file.txt"), "linked\n")
	observation := observeInventoryCommand(t, workspace, workspace, "make test", nil)
	inventory := observation.Inventory
	if !slices.Equal(inventory.Repositories, []string{"linked", "repo"}) || inventory.Blobs[filepath.Join("repo", "lib.txt")] == "" {
		t.Fatalf("nested repository not inventoried: repositories=%v blobs=%v", inventory.Repositories, inventory.Blobs)
	}
	for relative := range inventory.Entries {
		if filepath.Base(relative) == ".git" {
			t.Fatalf("inventory listed a metadata path: %s", relative)
		}
	}
	writeTestFile(t, filepath.Join(nested, "build", "new.o"), "new")
	writeTestFile(t, filepath.Join(nested, "lib.txt"), "after\n")
	writeTestFile(t, filepath.Join(workspace, "linked", ".git"), "gitdir: /elsewhere\n")
	reviews := inventoryReviews(t, observation, nil)
	if got, want := reviewSummary(workspace, reviews), []string{"Create repo/build/new.o", "Edit repo/lib.txt"}; !slices.Equal(got, want) {
		t.Fatalf("reviews = %v, want %v", got, want)
	}
	if diff := inventoryReview(t, workspace, reviews, "repo/lib.txt").Diff; !strings.Contains(diff, "-before") || !strings.Contains(diff, "+after") {
		t.Fatalf("nested tracked edit lacks its exact diff: %s", diff)
	}
}
