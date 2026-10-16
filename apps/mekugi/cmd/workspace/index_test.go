package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestModuleIndexWorkspaceReferences(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))
	members, err := workspaceMembers()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := applicationSelection(indexComposition, members)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectModuleReferences(members)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 25 || len(selected) != 24 || selected["modules/contextreset"] {
		t.Fatalf("workspace=%d selected=%d contextreset=%v", len(entries), len(selected), selected["modules/contextreset"])
	}
	records := make(map[string][]string)
	for _, entry := range entries {
		records[string(entry.Descriptor.ID)] = entry.Descriptor.Records
	}
	for owner, want := range map[string][]string{
		"changehistory": {"changes"}, "outputrecovery": {"cursor", "output"}, "commandsegments": {"commandsegments"}, "usage": {"usage"},
	} {
		if !slices.Equal(records[owner], want) {
			t.Fatalf("%s records=%v, want %v", owner, records[owner], want)
		}
	}
	if _, ok := records["contextreset"]; !ok {
		t.Fatal("unselected workspace module omitted")
	}
}

func TestModuleIndexWriteCheckAndDrift(t *testing.T) {
	contracts, err := filepath.Abs("../../../../contracts")
	if err != nil {
		t.Fatal(err)
	}
	workspaceFixture(t, []string{"./contracts", "./modules/second", "./modules/first"}, map[string]string{
		"contracts": prefix + "/contracts", "modules/first": prefix + "/modules/first", "modules/second": prefix + "/modules/second",
	})
	packages, err := os.ReadDir(contracts)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		if pkg.IsDir() {
			if err := os.CopyFS(filepath.Join("contracts", pkg.Name()), os.DirFS(filepath.Join(contracts, pkg.Name()))); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, id := range []string{"first", "second"} {
		writeWorkspaceSource(t, filepath.Join("modules", id, "module.go"), fmt.Sprintf(`package %s
import (
    "context"
    "github.com/yusing/mekugi/contracts/module"
    "github.com/yusing/mekugi/contracts/recordkind"
)
var Module = module.Descriptor[struct{}, int, module.NoBindings]{
    ID: %q, Summary: "fixture", Lifetime: module.Stateless,
    Contributes: module.Contributions[int]{RecordKinds: records()},
    New: func(struct{}, module.Slots) (int, error) { panic("constructor executed") },
    Start: func(context.Context, int) error { panic("lifecycle executed") },
    Stop: func(context.Context, int) error { panic("lifecycle executed") },
}
func records() []recordkind.Spec { return []recordkind.Spec{{ID: %q + "-record", Match: func(string) bool { panic("record callback executed") }}} }
`, id, id, id))
	}
	writeWorkspaceSource(t, indexComposition, `package composition
import feature "github.com/yusing/mekugi/modules/first"
func Frontends() {
    var c Composition
    c.Add(feature.Module, func() int { panic("dependencies executed") })
    c.Activate()
}`)
	writeWorkspaceSource(t, indexTemplate, `{{.SelectedCount}}/{{len .Modules}}{{range .Modules}} {{.Descriptor.ID}}:{{.Selected}}:{{range .Descriptor.Records}}{{.}}{{end}}{{end}}`)
	members, err := workspaceMembers()
	if err != nil {
		t.Fatal(err)
	}
	if err := moduleIndex(members, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(indexOutput)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1/2 first:true:first-record second:false:second-record"; string(got) != want {
		t.Fatalf("index=%q, want %q", got, want)
	}
	if err := moduleIndex(members, true); err != nil {
		t.Fatalf("unchanged generation: %v", err)
	}
	writeWorkspaceSource(t, indexOutput, "stale")
	if err := moduleIndex(members, true); err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("drift check=%v", err)
	}
	if unchanged, err := os.ReadFile(indexOutput); err != nil || string(unchanged) != "stale" {
		t.Fatalf("check modified retained output: %q, %v", unchanged, err)
	}
	if err := moduleIndex(members, false); err != nil {
		t.Fatal(err)
	}
	if regenerated, err := os.ReadFile(indexOutput); err != nil || string(regenerated) != string(got) {
		t.Fatalf("regeneration=%q, %v; want %q", regenerated, err, got)
	}
	writeWorkspaceSource(t, "go.work", "go 1.27.0\nuse (\n./contracts\n./modules/first\n)\n")
	members, err = workspaceMembers()
	if err != nil {
		t.Fatal(err)
	}
	if err := moduleIndex(members, false); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(indexOutput); err != nil || string(got) != "1/1 first:true:first-record" {
		t.Fatalf("workspace removal was not reflected: %q, %v", got, err)
	}
}

func TestModuleIndexSelectionRejectsUnsupportedShapes(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"conditional", "if enabled { c.Add(feature.Module, deps) }"},
		{"helper", "addModules(&c)"},
		{"descriptor variable", "c.Add(selected, deps)"},
		{"computed descriptor", "c.Add(feature.Descriptor(), deps)"},
		{"unknown member", "c.Add(other.Module, deps)"},
		{"shadowed import", "feature := alternate; c.Add(feature.Module, deps)"},
		{"other method", "c.AddModules(feature.Module)"},
		{"replacement", "c = another"},
		{"duplicate", "c.Add(feature.Module, deps); c.Add(feature.Module, deps)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "modules.go")
			writeWorkspaceSource(t, path, "package composition\nimport feature \""+prefix+"/modules/first\"\nfunc Frontends() { var c Composition; "+tt.body+"; c.Activate() }")
			if _, err := applicationSelection(path, []member{{"modules/first", prefix + "/modules/first"}}); err == nil {
				t.Fatal("unsupported composition accepted")
			}
		})
	}
}
