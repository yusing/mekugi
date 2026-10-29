package changehistory_test

import (
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/contracts/reference"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

type fixture struct {
	service   changes.Service
	history   *history.Service
	selected  changes.Scope
	workspace string
	run       command.Contribution
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// Immutable observations are contributed by the application's capture owner.
	kinds := append(slices.Clone(changehistory.Module.Contributes.RecordKinds), recordkind.Spec{
		ID: "fixture-call", Match: func(name string) bool { return strings.HasPrefix(name, "call-") && strings.HasSuffix(name, ".json") },
		Dependencies: func(record recordkind.Record) ([]string, error) {
			return legacycall.Dependencies(record.Name, record.Data)
		},
	})
	host := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{RecordKinds: kinds}, nil)
	store, err := host.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	script := &moduletest.Scripted{}
	service := moduletest.New(t, changehistory.Module, changehistory.Deps{History: host}, module.Slots{}, script)
	if commands := script.Commands(); len(commands) != 1 || commands[0].Name != "mchanges" {
		t.Fatalf("frontends: %+v", commands)
	}
	definitions := changehistory.Module.Contributes.Commands
	if len(definitions) != 1 || definitions[0].Name != "mchanges" {
		t.Fatalf("commands: %+v", definitions)
	}
	return &fixture{service: service, history: host, selected: changes.Scope{Store: store, Namespace: "owner", Thread: "owner"}, workspace: t.TempDir(), run: moduletest.BindCommand(definitions[0], service)}
}

func (f *fixture) reserve(t *testing.T, thread, correlation string) string {
	t.Helper()
	id, err := f.service.Reserve(t.Context(), f.selected, f.workspace, thread, correlation)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) publish(t *testing.T, id, correlation, call, thread string, files []legacycall.ReviewFile) {
	t.Helper()
	record := legacycall.Record{Version: 1, Workspace: f.workspace, CallID: call, History: legacycall.History{ToolName: "apply_patch", Root: f.workspace, Script: call, ChangeID: id, CorrelationID: correlation, ExecutingThread: thread, Attempt: 1, ReviewFiles: files}}
	data, err := json.Marshal(record, jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatal(err)
	}
	err = f.selected.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		// The immutable host observation is durable before membership is published.
		if err := tx.Write(legacycall.Name(f.workspace, call, false), "call-pending-", data); err != nil {
			return err
		}
		updates, err := f.service.Publish(tx, f.selected, f.workspace, map[string]changes.Publication{call: {ChangeID: id, CorrelationID: correlation, ExecutingThread: thread, Confirmed: true}})
		if err == nil && (len(updates) != 1 || updates[0].ID != id || len(updates[0].Change.Calls) != 1) {
			return fmt.Errorf("publication updates: %+v", updates)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) execute(t *testing.T, thread string, args ...string) command.Output {
	t.Helper()
	output, err := f.run.Run(t.Context(), command.Request{Args: args, Scope: scope.Retained{Directory: f.selected.Store.Directory(), Namespace: f.selected.Namespace, Thread: thread}})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func review(path, before, after string) legacycall.ReviewFile {
	return legacycall.ReviewFile{BeforePath: path, AfterPath: path, Diff: fmt.Sprintf("@@ -1 +1 @@\n-%s\n+%s\n", before, after)}
}

func TestModulePublicationScopedReadAndFrozenRecovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.reserve(t, "owner", "first")
	if repeated := f.reserve(t, "owner", "first"); repeated != first {
		t.Fatalf("correlation allocated twice: %s, %s", first, repeated)
	}
	path := filepath.Join(f.workspace, "first.txt")
	f.publish(t, first, "first", "first-call", "owner", []legacycall.ReviewFile{review(path, "old", "new")})
	sibling := f.reserve(t, "sibling", "sibling")
	f.publish(t, sibling, "sibling", "sibling-call", "sibling", []legacycall.ReviewFile{review(filepath.Join(f.workspace, "sibling.txt"), "before", "after")})
	pending := f.reserve(t, "owner", "pending")
	own := f.execute(t, "owner", "--list")
	if own.ExitCode != 0 || !strings.Contains(own.Stdout, first) || !strings.Contains(own.Stdout, pending) || strings.Contains(own.Stdout, sibling) {
		t.Fatalf("own membership: %+v", own)
	}
	cross := f.execute(t, "owner", sibling, "--summary")
	if cross.ExitCode != 0 || !strings.Contains(cross.Stdout, "sibling.txt") {
		t.Fatalf("explicit cross-agent read: %+v", cross)
	}
	partial := f.execute(t, "owner", first, "amber999", "--summary")
	if partial.ExitCode != 1 || !strings.Contains(partial.Stdout, "first.txt") || !strings.Contains(partial.Stderr, "amber999") {
		t.Fatalf("partial read: %+v", partial)
	}
	query, err := f.service.Parse([]string{"--mine", "--history"}, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	original, err := f.service.Read(t.Context(), f.selected, query)
	if err != nil {
		t.Fatal(err)
	}
	paged := f.execute(t, "owner", "--mine", "--history", "--max-tokens", "10")
	if paged.Delivery == nil || paged.Delivery.ReferenceCount() != 1 {
		t.Fatalf("missing frozen read plan: %+v", paged)
	}
	prepared, err := paged.Delivery.Prepare([]string{"reserved-read"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Recovery) != 1 || len(prepared.Recovery[0].ChangeSelection) == 0 || !strings.Contains(prepared.Referenced.Stderr, "mread reserved-read") {
		t.Fatalf("prepared read: %+v", prepared)
	}
	resolver := changehistory.Module.Contributes.References[0]
	selection, err := resolver.Canonicalize(prepared.Recovery[0].ChangeSelection, true)
	if err != nil {
		t.Fatal(err)
	}
	retainedScope := scope.Retained{Directory: f.selected.Store.Directory(), Namespace: "owner", Thread: "owner"}
	names, err := resolver.Dependencies(retainedScope, selection)
	if err != nil || !slices.Equal(names, []string{legacycall.Name(f.workspace, "first-call", false)}) {
		t.Fatalf("frozen dependencies: %v, %v", names, err)
	}
	// A completed pending change and a newly allocated change cannot alter this receipt.
	f.publish(t, pending, "pending", "pending-call", "owner", []legacycall.ReviewFile{review(filepath.Join(f.workspace, "later.txt"), "a", "b")})
	f.reserve(t, "owner", "new-pending")
	reopened := f.history.Attach(f.selected.Store.Directory())
	var omitted command.OmittedOutput
	err = reopened.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		if err := tx.Retain("owner", history.Limits{}, names...); err != nil {
			return err
		}
		var err error
		omitted, err = resolver.Resolve(t.Context(), reference.View{Scope: retainedScope, Retain: func(names ...string) error { return tx.Retain("owner", history.Limits{}, names...) }}, selection)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.Referenced.Stdout + omitted.Stdout; got != original || omitted.StdoutKind != "rows" {
		t.Fatalf("frozen recovery changed:\n%s\nwant:\n%s", got, original)
	}
}

func TestModuleLegacyEmptyViewRecovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.reserve(t, "owner", "legacy")
	f.publish(t, id, "legacy", "legacy-call", "owner", []legacycall.ReviewFile{review(filepath.Join(f.workspace, "file.txt"), "old", "new")})
	legacy := id + "\n@@ -1 +1 @@\n-old\n+new\n"
	paged := f.execute(t, "owner", id, "--max-tokens", "10")
	if paged.Delivery == nil {
		t.Fatalf("missing composed read delivery: %+v", paged)
	}
	prepared, err := paged.Delivery.Prepare([]string{"legacy-read"})
	if err != nil {
		t.Fatal(err)
	}
	var selection map[string]any
	if err := json.Unmarshal(prepared.Recovery[0].ChangeSelection, &selection); err != nil {
		t.Fatal(err)
	}
	if selection["view"] != "net" {
		t.Fatalf("new default selection view = %v", selection["view"])
	}
	selection["view"], selection["offset"], selection["digest"] = "", 1, fmt.Sprintf("%x", sha256.Sum256([]byte(legacy)))
	resolver := changehistory.Module.Contributes.References[0]
	for _, frozen := range []bool{false, true} {
		selection["frozen"] = frozen
		data, err := json.Marshal(selection)
		if err != nil {
			t.Fatal(err)
		}
		data, err = resolver.Canonicalize(data, true)
		if err != nil {
			t.Fatal(err)
		}
		omitted, err := resolver.Resolve(t.Context(), reference.View{Scope: scope.Retained{Directory: f.selected.Store.Directory(), Namespace: "owner", Thread: "owner"}, Retain: func(...string) error { return nil }}, data)
		if err != nil || omitted.Stdout != legacy[1:] {
			t.Fatalf("legacy frozen=%v recovery: %+v, %v", frozen, omitted, err)
		}
	}
}

func TestModuleMutationScopeAndPureReportRecovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.reserve(t, "owner", "edit")
	path := filepath.Join(f.workspace, "edit.txt")
	if err := os.WriteFile(path, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.publish(t, id, "edit", "edit-call", "owner", []legacycall.ReviewFile{review(path, "old", "new")})
	query, err := f.service.Parse([]string{"revert", id}, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := f.service.MutationPaths(t.Context(), f.selected, query)
	if err != nil || !slices.Equal(paths, []string{path}) {
		t.Fatalf("mutation paths: %v, %v", paths, err)
	}
	// Missing dependencies prevent every effect, even when another ID is readable.
	failed := f.execute(t, "owner", "revert", id, "amber999")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new\n" || failed.ExitCode != 1 {
		t.Fatalf("failed dependency mutated file: %q, %+v, %v", data, failed, err)
	}
	output := f.execute(t, "owner", "revert", id, "--max-tokens", "10")
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "old\n" || output.Delivery == nil {
		t.Fatalf("revert/report: %q, %+v, %v", data, output, err)
	}
	// Recovery plans carry only captured report output, never another mutation.
	prepared, err := output.Delivery.Prepare([]string{"report"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Recovery) != 1 || len(prepared.Recovery[0].ChangeSelection) != 0 || !strings.Contains(prepared.Referenced.Stdout+prepared.Recovery[0].Stdout, "undo: mchanges apply "+id) {
		t.Fatalf("report recovery: %+v", prepared)
	}
	if err := os.WriteFile(path, []byte("unobserved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Delivery.Prepare([]string{"other-report"}); err != nil {
		t.Fatal(err)
	}
	fallback, ok := output.Delivery.(command.RecoveryFailure)
	if !ok {
		t.Fatal("mutation report has no publication-failure fallback")
	}
	full := fallback.RecoveryFailed(errors.New("fixture retention failure"))
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "unobserved\n" || full.ExitCode != 0 || full.Delivery != nil || full.Stdout != prepared.Referenced.Stdout+prepared.Recovery[0].Stdout || !strings.Contains(full.Stderr, "fixture retention failure") {
		t.Fatalf("report recovery repeated effect or lost report: %q, %+v, %v", data, full, err)
	}
}

func TestModuleForkDependencyPublicationAndRetirement(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.reserve(t, "owner", "edit")
	f.publish(t, id, "edit", "call", "owner", []legacycall.ReviewFile{review(filepath.Join(f.workspace, "edit.txt"), "old", "new")})
	child := f.selected
	child.Namespace, child.Thread = "fork", "fork"
	var plan changes.ForkPlan
	err := f.selected.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		var err error
		plan, err = f.service.PrepareFork(child, "owner", "fork", "owner")
		if err != nil {
			return err
		}
		names := plan.Dependencies()
		if !slices.Equal(names, []string{legacycall.Name(f.workspace, "call", false)}) {
			return fmt.Errorf("fork dependencies: %v", names)
		}
		if err := tx.Retain("fork", history.Limits{}, names...); err != nil {
			return err
		}
		if err := plan.Publish(tx); err != nil {
			return err
		}
		return tx.WriteScope(history.Scope{Version: 1, Thread: "fork", Namespace: "fork", Fork: "owner"})
	})
	if err != nil {
		t.Fatal(err)
	}
	inherited, err := f.service.Inspect(child, f.workspace)
	if err != nil || len(inherited.Changes[id].Calls) != 1 || len(inherited.Streams) != 1 || inherited.Streams[0].Thread != "fork" {
		t.Fatalf("fork membership: %+v, %v", inherited, err)
	}
	for _, admitted := range []bool{true, false} {
		visible, err := f.service.InspectVisible(child, f.workspace, map[string]bool{"fork": admitted})
		if err != nil || len(visible) != 1 {
			t.Fatalf("fork visible indexes: %+v, %v", visible, err)
		}
		if got := len(visible[0].Changes[id].Calls); (got == 1) != admitted || got > 1 {
			t.Fatalf("fork admitted=%v: visible calls=%d", admitted, got)
		}
	}
	inheritedText, err := f.service.Read(t.Context(), child, changes.Query{Workspace: f.workspace, Mine: true, View: "summary"})
	if err != nil || !strings.Contains(inheritedText, "edit.txt") {
		t.Fatalf("fork own-thread read: %q, %v", inheritedText, err)
	}
	f.reserve(t, "owner", "after-fork")
	inherited, err = f.service.Inspect(child, f.workspace)
	if err != nil || len(inherited.Changes) != 1 {
		t.Fatalf("fork inherited later allocation: %+v, %v", inherited, err)
	}
	// Exercise the contributed semantic planner against the fork's durable record.
	entries, err := os.ReadDir(f.selected.Store.Directory())
	if err != nil {
		t.Fatal(err)
	}
	var name string
	var planner recordkind.RetirementPlanner
	for _, entry := range entries {
		kind, found, err := f.history.Kind(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !found || kind.Retirement == nil {
			continue
		}
		data, err := history.ReadRecord(filepath.Join(f.selected.Store.Directory(), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var identity struct{ Namespace string }
		if err := json.Unmarshal(data, &identity); err != nil {
			t.Fatal(err)
		}
		if identity.Namespace != "fork" {
			continue
		}
		name = entry.Name()
		planner, err = kind.Retirement(t.Context(), recordkind.Record{Name: name, Data: data})
		if err != nil {
			t.Fatal(err)
		}
	}
	if planner == nil {
		t.Fatal("fork record retirement contribution missing")
	}
	if update, err := planner(t.Context(), recordkind.Retirement{Deleted: map[string]bool{"unrelated": true}}); err != nil || update != nil {
		t.Fatalf("unrelated deletion retired evidence: %+v, %v", update, err)
	}
	update, err := planner(t.Context(), recordkind.Retirement{Deleted: map[string]bool{legacycall.Name(f.workspace, "call", false): true}})
	if err != nil || update == nil {
		t.Fatalf("confirmed retirement: %+v, %v", update, err)
	}
	// Keep the shared parent payload; this is the fork's semantic deletion fact.
	err = f.selected.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error { return tx.Write(name, "retired-pending-", update.Data) })
	if err != nil {
		t.Fatal(err)
	}
	retired, err := f.service.Inspect(child, f.workspace)
	if err != nil || len(retired.Changes) != 0 || retired.Streams[0].Next != 1 || retired.Streams[0].Retired != 1 {
		t.Fatalf("retired allocation lost: %+v, %v", retired, err)
	}
	text, err := f.service.Read(t.Context(), child, changes.Query{Workspace: f.workspace, IDs: []string{id}})
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("retired read: %q, %v", text, err)
	}
	next, err := f.service.Reserve(t.Context(), child, f.workspace, "fork", "new")
	if err != nil || next == id {
		t.Fatalf("retired identity reused: %q, %v", next, err)
	}
}

func TestInspectVisibleFiltersCallsAtOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	own := f.reserve(t, "owner", "own")
	f.publish(t, own, "own", "own-call", "owner", nil)
	other := f.reserve(t, "sibling", "other")
	f.publish(t, other, "other", "other-call", "sibling", nil)
	for _, selection := range []struct {
		name               string
		threads            map[string]bool
		wantOwn, wantOther int
	}{
		{"owner", map[string]bool{"owner": true}, 1, 0},
		{"all", nil, 1, 1},
	} {
		t.Run(selection.name, func(t *testing.T) {
			indexes, err := f.service.InspectVisible(f.selected, f.workspace, selection.threads)
			if err != nil {
				t.Fatal(err)
			}
			if len(indexes) != 1 {
				t.Fatalf("visible indexes: %+v", indexes)
			}
			if gotOwn, gotOther := len(indexes[0].Changes[own].Calls), len(indexes[0].Changes[other].Calls); gotOwn != selection.wantOwn || gotOther != selection.wantOther {
				t.Fatalf("visible calls: own=%d sibling=%d", gotOwn, gotOther)
			}
		})
	}
	unfiltered, err := f.service.Inspect(f.selected, f.workspace)
	if err != nil || len(unfiltered.Changes[other].Calls) != 1 {
		t.Fatal("visible inspection changed retained membership", err)
	}
}

func TestModuleWorkspaceIndependentSelectionAndMutation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	root := f.workspace
	f.workspace = filepath.Join(root, "bar")
	if err := os.Mkdir(f.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	first := f.reserve(t, "owner", "first")
	firstPath := filepath.Join(f.workspace, "baz1")
	f.publish(t, first, "first", "first-call", "owner", []legacycall.ReviewFile{review("baz1", "old", "new")})
	second := f.reserve(t, "owner", "second")
	secondPath := filepath.Join(f.workspace, "baz2")
	f.publish(t, second, "second", "second-call", "owner", []legacycall.ReviewFile{review("baz2", "before", "after")})
	for _, view := range []string{"", "--summary", "--history"} {
		var expected string
		for _, paths := range [][]string{nil, {"bar/baz1", "bar/baz2"}, {"baz1", "baz2"}, {firstPath, secondPath}} {
			args := []string{first + ".." + second}
			if view != "" {
				args = append(args, view)
			}
			args = append(args, paths...)
			query, err := f.service.Parse(args, root)
			if err != nil {
				t.Fatal(err)
			}
			text, err := f.service.Read(t.Context(), f.selected, query)
			if err != nil || text == "" {
				t.Fatalf("%s %v: %q, %v", view, paths, text, err)
			}
			if expected != "" && text != expected {
				t.Fatalf("%s %v: %q; want %q", view, paths, text, expected)
			}
			expected = text
		}
	}
	// Authored-relative paths win when the cwd spelling names another captured file.
	ambiguous := f.reserve(t, "sibling", "ambiguous")
	f.publish(t, ambiguous, "ambiguous", "ambiguous-call", "sibling", []legacycall.ReviewFile{review("bar/baz1", "inside", "preferred")})
	precedence, err := f.service.Parse([]string{first, ambiguous, "bar/baz1"}, root)
	if err != nil {
		t.Fatal(err)
	}
	preferred, err := f.service.Read(t.Context(), f.selected, precedence)
	if err != nil || !strings.Contains(preferred, "+preferred") || strings.Contains(preferred, "+new") {
		t.Fatalf("authored path precedence: %q, %v", preferred, err)
	}
	// A shared name selects the matching files in both authored workspaces.
	f.workspace = root
	other := f.reserve(t, "owner", "other")
	// Call identity is workspace-qualified, so this root may reuse first-call.
	f.publish(t, other, "other", "first-call", "owner", []legacycall.ReviewFile{review("baz1", "outside", "changed")})
	query, err := f.service.Parse([]string{first, other, "--summary", "baz1"}, root)
	if err != nil {
		t.Fatal(err)
	}
	text, err := f.service.Read(t.Context(), f.selected, query)
	if err != nil || !strings.Contains(text, "bar/baz1") || !strings.Contains(text, "\tbaz1\n") {
		t.Fatalf("same spelling in two authored workspaces: %q, %v", text, err)
	}
	sibling := f.reserve(t, "sibling", "sibling")
	f.publish(t, sibling, "sibling", "sibling-call", "sibling", []legacycall.ReviewFile{review("private", "a", "b")})
	// Reopen storage and select from unrelated cwd, using only retained caller evidence.
	reopened := f.history.Attach(f.selected.Store.Directory())
	selected := f.selected
	selected.Store = reopened
	query, err = f.service.Parse([]string{"--list"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	text, err = f.service.Read(t.Context(), selected, query)
	if err != nil || !strings.Contains(text, first+".."+second) || !strings.Contains(text, other) || strings.Contains(text, sibling) || strings.Contains(text, ambiguous) {
		t.Fatalf("retained caller workspaces: %q, %v", text, err)
	}
	// Frozen continuation retains each authored workspace and resolved filter.
	full := f.execute(t, "owner", first, other, "--history", "baz1")
	paged := f.execute(t, "owner", first, other, "--history", "--max-tokens", "10", "baz1")
	if full.ExitCode != 0 || paged.Delivery == nil {
		t.Fatalf("mixed-workspace paging: full=%+v, paged=%+v", full, paged)
	}
	prepared, err := paged.Delivery.Prepare([]string{"mixed-read"})
	if err != nil {
		t.Fatal(err)
	}
	resolver := changehistory.Module.Contributes.References[0]
	selection, err := resolver.Canonicalize(prepared.Recovery[0].ChangeSelection, true)
	if err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Streams    []changes.Stream  `json:"streams"`
		Workspaces map[string]string `json:"workspaces"`
	}
	if err := json.Unmarshal(selection, &frozen); err != nil || len(frozen.Streams) != 2 || len(frozen.Workspaces) != 2 {
		t.Fatalf("frozen selection retained unrelated streams: %+v, %v", frozen, err)
	}
	retainedScope := scope.Retained{Directory: reopened.Directory(), Namespace: "owner", Thread: "owner"}
	names, err := resolver.Dependencies(retainedScope, selection)
	if err != nil || !slices.Contains(names, legacycall.Name(filepath.Join(root, "bar"), "first-call", false)) || !slices.Contains(names, legacycall.Name(root, "first-call", false)) {
		t.Fatalf("mixed-workspace frozen dependencies: %v, %v", names, err)
	}
	omitted, err := resolver.Resolve(t.Context(), reference.View{Scope: retainedScope}, selection)
	if err != nil || prepared.Referenced.Stdout+omitted.Stdout != full.Stdout {
		t.Fatalf("mixed-workspace frozen continuation: %+v, %v", omitted, err)
	}
	for path, content := range map[string]string{firstPath: "new\n", secondPath: "after\n", filepath.Join(root, "baz1"): "changed\n"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	query, err = f.service.Parse([]string{"revert", first, other}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := f.service.MutationPaths(t.Context(), selected, query)
	if err != nil || !slices.Contains(paths, firstPath) || !slices.Contains(paths, filepath.Join(root, "baz1")) {
		t.Fatalf("mixed-workspace declared scope: %v, %v", paths, err)
	}
	for _, operation := range []string{"revert", "apply"} {
		output := f.execute(t, "owner", operation, first, other)
		want := "old\n"
		if operation == "apply" {
			want = "new\n"
		}
		data, err := os.ReadFile(firstPath)
		otherData, otherErr := os.ReadFile(filepath.Join(root, "baz1"))
		otherWant := "outside\n"
		if operation == "apply" {
			otherWant = "changed\n"
		}
		if err != nil || otherErr != nil || output.ExitCode != 0 || string(data) != want || string(otherData) != otherWant {
			t.Fatalf("mixed-workspace %s: %+v, %q, %v", operation, output, data, err)
		}
	}
	f.workspace = t.TempDir()
	missing := f.reserve(t, "owner", "missing-workspace")
	f.publish(t, missing, "missing-workspace", "missing-call", "owner", []legacycall.ReviewFile{review("gone", "a", "b")})
	if err := os.Remove(f.workspace); err != nil {
		t.Fatal(err)
	}
	failed := f.execute(t, "owner", "revert", first, missing)
	data, err := os.ReadFile(firstPath)
	if err != nil || failed.ExitCode != 1 || string(data) != "new\n" || !strings.Contains(failed.Stderr, "authored workspace") {
		t.Fatalf("missing authored workspace allowed partial writes: %+v, %q, %v", failed, data, err)
	}
}

func TestModuleReadIsolatesCorruptIndexes(t *testing.T) {
	t.Parallel()
	for _, namespace := range []string{"owner", "foreign"} {
		t.Run(namespace, func(t *testing.T) {
			f := newFixture(t)
			id := f.reserve(t, "owner", "good")
			f.publish(t, id, "good", "good-call", "owner", []legacycall.ReviewFile{review("good.txt", "old", "new")})
			name := fmt.Sprintf("changes-v3-%x.json", sha256.Sum256([]byte(namespace+"\x00"+t.TempDir())))
			data := fmt.Sprintf(`{"Namespace":%q,"Version":"broken-schema"}`, namespace)
			if namespace == "foreign" {
				data = `{"Namespace":`
			}
			if err := os.WriteFile(filepath.Join(f.selected.Store.Directory(), name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			out := f.execute(t, "owner", id, "--summary")
			if out.ExitCode != 0 || !strings.Contains(out.Stdout, "good.txt") {
				t.Fatalf("unrelated index suppressed healthy selection: %+v", out)
			}
			unknown := changes.ID("z", 1)
			for _, args := range [][]string{{id, unknown, "--summary"}, {"--mine", "--summary"}} {
				out = f.execute(t, "owner", args...)
				if out.ExitCode != 1 || !strings.Contains(out.Stdout, "good.txt") || !strings.Contains(out.Stderr, "discovery") {
					t.Fatalf("incomplete discovery lost healthy evidence or diagnostic: %+v", out)
				}
			}
			path := filepath.Join(f.workspace, "good.txt")
			if err := os.WriteFile(path, []byte("new\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if out = f.execute(t, "owner", "revert", id); out.ExitCode != 0 {
				t.Fatalf("unrelated corrupt index blocked a resolved mutation: %+v", out)
			}
			out = f.execute(t, "owner", "apply", id, unknown)
			content, err := os.ReadFile(path)
			if err != nil || out.ExitCode != 1 || string(content) != "old\n" {
				t.Fatalf("unresolved ownership allowed partial mutation: %+v, %q, %v", out, content, err)
			}
		})
	}
}

func TestModuleSymlinkCallerFilters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	root := f.workspace
	f.workspace = filepath.Join(root, "bar")
	if err := os.Mkdir(f.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	id := f.reserve(t, "owner", "good")
	f.publish(t, id, "good", "good-call", "owner", []legacycall.ReviewFile{review("baz", "old", "new")})
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	query, err := f.service.Parse([]string{id, "bar/baz"}, alias)
	if err != nil {
		t.Fatal(err)
	}
	text, err := f.service.Read(t.Context(), f.selected, query)
	if err != nil || !strings.Contains(text, "+new") {
		t.Fatalf("cwd-relative filter through symlink lost evidence: %q, %v", text, err)
	}
}

func TestModuleReadIsolatesAmbiguousOwnership(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	firstRoot := f.workspace
	first := f.reserve(t, "owner", "one")
	f.publish(t, first, "one", "one-call", "owner", []legacycall.ReviewFile{review("one.txt", "a", "b")})
	f.workspace = t.TempDir()
	secondRoot := f.workspace
	second := f.reserve(t, "owner", "two")
	f.publish(t, second, "two", "two-call", "owner", []legacycall.ReviewFile{review("two.txt", "c", "d")})
	f.workspace = t.TempDir()
	healthy := f.reserve(t, "owner", "healthy")
	f.publish(t, healthy, "healthy", "healthy-call", "owner", []legacycall.ReviewFile{review("healthy.txt", "x", "y")})
	index, err := f.service.Inspect(f.selected, firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	index.Workspace = secondRoot
	data, err := json.Marshal(struct {
		Version int
		changes.Snapshot
	}{Version: 1, Snapshot: index}, jsonv1.DefaultOptionsV1())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("changes-v3-%x.json", sha256.Sum256([]byte("owner\x00"+secondRoot)))
	if err := os.WriteFile(filepath.Join(f.selected.Store.Directory(), name), data, 0600); err != nil {
		t.Fatal(err)
	}
	out := f.execute(t, "owner", healthy, "--summary")
	if out.ExitCode != 0 || !strings.Contains(out.Stdout, "healthy.txt") {
		t.Fatalf("unselected ambiguity suppressed healthy ownership: %+v", out)
	}
	out = f.execute(t, "owner", first, healthy, "--summary")
	if out.ExitCode != 1 || !strings.Contains(out.Stdout, "healthy.txt") || strings.Contains(out.Stdout, "one.txt") || !strings.Contains(out.Stderr, "ambiguous") {
		t.Fatalf("ambiguous ownership lost independent result or remained usable: %+v", out)
	}
	path := filepath.Join(f.workspace, "healthy.txt")
	if err := os.WriteFile(path, []byte("y\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out = f.execute(t, "owner", "revert", first, healthy)
	content, err := os.ReadFile(path)
	if err != nil || out.ExitCode != 1 || string(content) != "y\n" {
		t.Fatalf("ambiguous ownership allowed partial writes: %+v, %q, %v", out, content, err)
	}
	if out = f.execute(t, "owner", "revert", healthy); out.ExitCode != 0 {
		t.Fatalf("unrelated ambiguity blocked healthy mutation: %+v", out)
	}
}
