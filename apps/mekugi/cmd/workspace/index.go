package main

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/yusing/mekugi/contracts/module"
)

const (
	indexTemplate    = "apps/mekugi/cmd/workspace/modules.md.tmpl"
	indexOutput      = "docs/reference/modules.md"
	indexComposition = "apps/mekugi/internal/composition/modules.go"
)

type indexData struct {
	Modules       []indexModule
	SelectedCount int
}

type indexModule struct {
	Directory  string
	Selected   bool
	Descriptor module.Reference
	Binding    module.BindingReference
}

func moduleIndex(members []member, check bool) error {
	selected, err := applicationSelection(indexComposition, members)
	if err != nil {
		return err
	}
	entries, err := collectModuleReferences(members)
	if err != nil {
		return err
	}
	data := indexData{Modules: entries, SelectedCount: len(selected)}
	for i := range data.Modules {
		data.Modules[i].Selected = selected[data.Modules[i].Directory]
	}
	slices.SortFunc(data.Modules, func(a, b indexModule) int {
		return strings.Compare(string(a.Descriptor.ID), string(b.Descriptor.ID))
	})
	tmpl, err := template.ParseFiles(indexTemplate)
	if err != nil {
		return err
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return err
	}
	if check {
		retained, err := os.ReadFile(indexOutput)
		if err != nil {
			return err
		}
		if !bytes.Equal(retained, output.Bytes()) {
			return fmt.Errorf("%s is stale; run make generate", indexOutput)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(indexOutput), 0755); err != nil {
		return err
	}
	return os.WriteFile(indexOutput, output.Bytes(), 0644)
}

// collectModuleReferences imports descriptors selected by workspace membership.
// Only package initialization and callback-free Reference methods execute.
func collectModuleReferences(members []member) ([]indexModule, error) {
	var features []member
	for _, m := range members {
		if strings.HasPrefix(filepath.ToSlash(m.directory), "modules/") {
			features = append(features, m)
		}
	}
	var source strings.Builder
	source.WriteString("package main\nimport (\n json \"encoding/json/v2\"\n \"os\"\n \"github.com/yusing/mekugi/contracts/module\"\n")
	for i, m := range features {
		fmt.Fprintf(&source, " feature%d %q\n", i, m.path)
	}
	source.WriteString(")\ntype entry struct { Directory string; Descriptor module.Reference; Binding module.BindingReference }\nfunc main() {\n entries := []entry{\n")
	for i, m := range features {
		fmt.Fprintf(&source, " {Directory: %q, Descriptor: feature%d.Module.Reference(), Binding: feature%d.Module.Bindings.Reference()},\n", filepath.ToSlash(m.directory), i, i)
	}
	source.WriteString("}\n data, err := json.Marshal(entries); if err != nil { panic(err) }; if _, err := os.Stdout.Write(data); err != nil { panic(err) }\n}\n")
	directory, err := os.MkdirTemp("", "mekugi-module-reference-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	collector := filepath.Join(directory, "main.go")
	if err := os.WriteFile(collector, []byte(source.String()), 0600); err != nil {
		return nil, err
	}
	workspace, err := filepath.Abs("go.work")
	if err != nil {
		return nil, err
	}
	command := exec.Command("go", "run", "-trimpath", collector)
	command.Env = append(os.Environ(), "GOWORK="+workspace)
	output, err := command.Output()
	if err != nil {
		if failure, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("collect module references: %w: %s", err, strings.TrimSpace(string(failure.Stderr)))
		}
		return nil, fmt.Errorf("collect module references: %w", err)
	}
	var entries []indexModule
	if err := json.Unmarshal(output, &entries); err != nil {
		return nil, fmt.Errorf("decode module references: %w", err)
	}
	if len(entries) != len(features) {
		return nil, fmt.Errorf("collected %d module references for %d workspace modules", len(entries), len(features))
	}
	for i, entry := range entries {
		if entry.Descriptor.ID != module.ID(filepath.Base(features[i].directory)) {
			return nil, fmt.Errorf("%s: descriptor identity %q must match workspace member", entry.Directory, entry.Descriptor.ID)
		}
	}
	return entries, nil
}
