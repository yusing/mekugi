package composition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/activity/waittargets"
	"github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory"
)

func TestFrontendsRetainedSegmentsWithoutTransport(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "disabled"
		if live {
			name = "unavailable"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			temp := root
			if live {
				// A regular file makes transport acquisition fail even as root.
				temp = filepath.Join(root, "not-a-directory")
				if err := os.WriteFile(temp, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TMPDIR", temp)
			runtime, err := Frontends(t.Context(), live, true, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := runtime.Close(t.Context()); err != nil {
					t.Error(err)
				}
			})
			if runtime.Segments.Live != nil || (runtime.Segments.LiveError != nil) != live {
				t.Fatalf("optional transport: live=%v err=%v", runtime.Segments.Live, runtime.Segments.LiveError)
			}
			entries, err := os.ReadDir(root)
			wantEntries := 0
			if live {
				wantEntries = 1
			}
			if err != nil || len(entries) != wantEntries {
				t.Fatalf("composition acquired temporary resources: entries=%v err=%v", entries, err)
			}
			workspace := t.TempDir()
			directory := t.TempDir()
			id := reports.ID("turn", "item")
			name := reports.Name(workspace, id)
			kind, found, err := runtime.History.Kind(name)
			if err != nil || !found || kind.ID != "commandsegments" {
				t.Fatalf("record contribution: kind=%+v found=%v err=%v", kind, found, err)
			}
			scope := reports.Scope{Store: runtime.History.Attach(directory), Workspace: workspace, Thread: "viewer"}
			report := reports.Report{Command: "echo ok", Parts: []reports.Part{{Source: "echo ok"}}}
			if err := runtime.Segments.Reports.Publish(t.Context(), scope, id, report); err != nil {
				t.Fatal(err)
			}
			if _, found, err := runtime.Segments.Reports.Inspect(directory, workspace, id); err != nil || !found {
				t.Fatalf("retained API without transport: found=%v err=%v", found, err)
			}
		})
	}
}

func TestCompositionCollectsRecordKindsBeforeHostConstruction(t *testing.T) {
	later := recordkind.Spec{ID: "later", Match: func(name string) bool { return name == "later.json" }, Keep: true}
	var c Composition
	host := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	contributor := descriptor("later")
	contributor.Contributes.RecordKinds = []recordkind.Spec{later}
	contributor.New = func(_ struct{}, slots module.Slots) (*testService, error) {
		if len(slots.RecordKinds) != 0 {
			t.Fatal("non-host received record contributions")
		}
		return new(testService), nil
	}
	c.Add(contributor, emptyDeps)
	runtime, err := c.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	kind, found, err := host.Value().Kind("later.json")
	if err != nil || !found || kind.ID != later.ID || kind.Keep != later.Keep {
		t.Fatalf("collected later: kind=%+v found=%v err=%v", kind, found, err)
	}
}

func TestFrontendsExposesConstructedHistoryHost(t *testing.T) {
	runtime, err := Frontends(t.Context(), false, true, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if runtime.History == nil {
		t.Fatal("application omitted the history host")
	}
	for _, tc := range []struct{ name, id string }{
		{legacycall.Name("/workspace", "call", false), "call"},
		{legacycall.Name("/workspace", "commentary", true), "commentary"},
		{legacycall.SnapshotName([]byte("snapshot")), "snapshot"},
		{waittargets.Name("/workspace", "thread", "turn", "item"), "wait"},
	} {
		kind, found, err := runtime.History.Kind(tc.name)
		if err != nil || !found || kind.ID != tc.id {
			t.Fatalf("%s contribution: kind=%+v found=%v err=%v", tc.id, kind, found, err)
		}
		if kind.SeparateBudget != (tc.id == "commentary") || tc.id == "commentary" && kind.Limit != 16<<20 {
			t.Fatalf("%s budget changed: %+v", tc.id, kind)
		}
		if tc.id == "call" {
			if kind.Dependencies == nil {
				t.Fatal("call contribution omitted snapshot dependency validation")
			}
			ref := legacycall.SnapshotName([]byte("snapshot"))
			data := []byte(`{"Version":2,"Workspace":"/workspace","CallID":"call","Snapshots":{"ExecFiles":"` + ref + `"}}`)
			deps, err := kind.Dependencies(recordkind.Record{Name: tc.name, Data: data})
			if err != nil || len(deps) != 1 || deps[0] != ref {
				t.Fatalf("call dependencies: %v, %v", deps, err)
			}
		}
		prefix, suffix, _ := strings.Cut(tc.name, "-")
		for _, invalid := range []string{"../" + tc.name, prefix + "-" + strings.Repeat("A", 64) + suffix[64:], prefix + "-" + strings.Repeat("g", 64) + suffix[64:], strings.Replace(tc.name, "-", "-a", 1), tc.name + ".tmp"} {
			if kind.Match(invalid) {
				t.Fatalf("invalid record %q recognized as %s", invalid, kind.ID)
			}
		}
	}
	if runtime.Segments == nil {
		t.Fatal("application omitted the command-segment owner")
	}
	kind, found, err := runtime.History.Kind(reports.Name("/workspace", reports.ID("turn", "item")))
	if err != nil || !found || kind.ID != "commandsegments" {
		t.Fatalf("command segment contribution: kind=%+v found=%v err=%v", kind, found, err)
	}

}

func TestFrontendsOwnsWorkspaceCapture(t *testing.T) {
	directory := t.TempDir()
	runtime, err := Frontends(t.Context(), false, true, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	snapshots, err := runtime.Changes.Checkpoints(directory)
	if err != nil || snapshots == nil {
		t.Fatalf("composed checkpoints: %v %v", snapshots, err)
	}
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	observation, observed := capture.Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: workspace}}, false, false, capture.Options{Directory: workspace, Workspaces: snapshots})
	if !observed || observation == nil || observation.Tree == "" {
		t.Fatalf("composed baseline: %+v", observation)
	}
	if err := os.WriteFile(path, []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := capture.Reconcile(t.Context(), *observation, capture.Comparison{Workspace: workspace, Workspaces: snapshots})
	if result.SnapshotError != nil || len(result.Files) != 1 || !strings.Contains(result.Files[0].Diff, "+after") {
		t.Fatalf("composed result: %+v", result)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Changes.Checkpoints(directory); err == nil {
		t.Fatal("composition left capture owner available")
	}
}
