package composition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
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
			runtime, err := Frontends(t.Context(), live)
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
	legacy := recordkind.Spec{ID: "legacy", Match: func(name string) bool { return name == "legacy.json" }}
	later := recordkind.Spec{ID: "later", Match: func(name string) bool { return name == "later.json" }, Keep: true}
	c := Composition{LegacyRecordKinds: []recordkind.Spec{legacy}}
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
	for _, want := range []recordkind.Spec{legacy, later} {
		kind, found, err := host.Value().Kind(want.ID + ".json")
		if err != nil || !found || kind.ID != want.ID || kind.Keep != want.Keep {
			t.Fatalf("collected %s: kind=%+v found=%v err=%v", want.ID, kind, found, err)
		}
	}
}

func TestFrontendsExposesConstructedHistoryHost(t *testing.T) {
	runtime, err := Frontends(t.Context(), false)
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
	kind, found, err := runtime.History.Kind("call-" + strings.Repeat("a", 64) + ".json")
	if err != nil || !found || kind.ID != "call" || kind.Dependencies == nil {
		t.Fatalf("legacy call contribution: kind=%+v found=%v err=%v", kind, found, err)
	}
	if runtime.Segments == nil {
		t.Fatal("application omitted the command-segment owner")
	}
	kind, found, err = runtime.History.Kind(reports.Name("/workspace", reports.ID("turn", "item")))
	if err != nil || !found || kind.ID != "commandsegments" {
		t.Fatalf("command segment contribution: kind=%+v found=%v err=%v", kind, found, err)
	}

}
