package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkspaceSource(t *testing.T, path, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceImportBoundaries(t *testing.T) {
	for _, tt := range []struct {
		owner, pkg, target string
		allowed            bool
	}{
		{"libraries/outputbudget", ".", "libraries/sourceanalysis/rows", true},
		{"libraries/terminalui", ".", "libraries/sourceanalysis/markdownsyntax", true},
		{"libraries/sourceanalysis", "markdownsyntax", "libraries/terminalui", false},
		{"libraries/diffrender", ".", "libraries/textsanitize", true},
		{"libraries/terminalui", ".", "libraries/textsanitize", true},
		{"libraries/textsanitize", ".", "libraries/diffrender", false},
		{"modules/activity", "presentation", "libraries/textsanitize", true},
		{"libraries/outputbudget", ".", "backends/codex/binding", false},
		{"libraries/pathdisplay", ".", "libraries/persistence", false},
		{"backends/codex", "binding", "contracts/command", true},
		{"backends/codex", "binding", "providers/openai", false},
		{"providers/openai", ".", "libraries/persistence", true},
		{"providers/openai", ".", "backends/codex/binding", false},
		{"contracts", "command", "modules/reader", false},
		{"testsupport/moduletest", ".", "backends/codex/binding", true},
		{"apps/mekugi", "internal/composition", "testsupport/moduletest", false},
		{"modules/reader", "codexbinding", "backends/codex/binding", true},
		{"modules/reader", "internal", "backends/codex/binding", false},
		{"modules/reader", "terminalview", "libraries/terminalui", true},
		{"modules/reader", "presentation", "libraries/terminalui", false},
		{"modules/reader", "presentation", "modules/reader/terminalview", false},
		{"modules/reader", "internal", "modules/reader/codexbinding", false},
	} {
		t.Run(tt.owner+"/"+tt.pkg+"/"+tt.target, func(t *testing.T) {
			target := prefix + "/" + tt.target
			if err := allowedImport(tt.owner, tt.pkg, target); (err == nil) != tt.allowed {
				t.Fatalf("allowedImport error=%v want allowed=%v", err, tt.allowed)
			}
			workspaceFixture(t, []string{"./" + tt.owner}, map[string]string{tt.owner: prefix + "/" + tt.owner})
			if strings.HasPrefix(tt.owner, "modules/") {
				writeWorkspaceSource(t, filepath.Join(tt.owner, "module.go"), `package reader
import "github.com/yusing/mekugi/contracts/module"
type Deps struct{}
var Module = module.Descriptor[Deps, int, int]{}
`)
			}
			path := filepath.Join(tt.owner, tt.pkg, "source.go")
			writeWorkspaceSource(t, path, fmt.Sprintf("package fixture\nimport _ %q\n", target))
			err := checkImports([]member{{tt.owner, prefix + "/" + tt.owner}})
			if (err == nil) != tt.allowed {
				t.Fatalf("checkImports error=%v want allowed=%v", err, tt.allowed)
			}
			if !tt.allowed && (!strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), target)) {
				t.Fatalf("failure lacks source/import identity: %v", err)
			}
		})
	}
}

func TestAllowedModulePublicAndRendererImports(t *testing.T) {
	for _, tt := range []struct {
		pkg, target string
		allowed     bool
	}{
		{"internal", "modules/history/publication", true},
		{"internal", "modules/history", false},
		{"internal", "modules/history/internal/records", false},
		{"internal", "modules/history/codexbinding", false},
		{"internal", "modules/history/terminalview", false},
		{"internal", "modules/history/presentation", false},
		{"codexbinding", "backends/codex/internal", false},
		{"presentation", "github.com/charmbracelet/lipgloss", false},
		{"terminalview", "github.com/charmbracelet/lipgloss", true},
		{"presentation", "golang.org/x/term", false},
	} {
		t.Run(tt.pkg+"/"+tt.target, func(t *testing.T) {
			target := tt.target
			if !strings.Contains(target, ".") {
				target = prefix + "/" + target
			}
			if err := allowedImport("modules/reader", tt.pkg, target); (err == nil) != tt.allowed {
				t.Fatalf("error=%v want allowed=%v", err, tt.allowed)
			}
		})
	}
}

func TestWorkspaceChecksAllProductionPlatformsButNotTests(t *testing.T) {
	const owner = "libraries/pathdisplay"
	workspaceFixture(t, []string{"./" + owner}, map[string]string{owner: prefix + "/" + owner})
	forbidden := prefix + "/backends/codex/binding"
	source := fmt.Sprintf("package fixture\nimport _ %q\n", forbidden)
	writeWorkspaceSource(t, filepath.Join(owner, "source_test.go"), source)
	if err := checkImports([]member{{owner, prefix + "/" + owner}}); err != nil {
		t.Fatalf("test-only import rejected: %v", err)
	}
	tagged := filepath.Join(owner, "source_plan9.go")
	writeWorkspaceSource(t, tagged, "//go:build plan9\n\n"+source)
	if err := checkImports([]member{{owner, prefix + "/" + owner}}); err == nil || !strings.Contains(err.Error(), tagged) {
		t.Fatalf("platform-specific production import escaped: %v", err)
	}
}

func TestModuleRequiresMatchesImportsAndTypedDeps(t *testing.T) {
	const owner = "modules/reader"
	const template = `package reader
import (
 "github.com/yusing/mekugi/contracts/module"
 retained "github.com/yusing/mekugi/modules/history/publication"
)
type Deps struct{%s}
var Module = module.Descriptor[Deps, int, int]{Requires: %s}
`
	for _, tt := range []struct{ name, deps, requires, extra, reason string }{
		{"matching", "History retained.Publisher", `[]module.ID{"history"}`, "", ""},
		{"missing requirement", "History retained.Publisher", `[]module.ID{}`, "", "must match"},
		{"missing typed dependency", "", `[]module.ID{"history"}`, "", "must match"},
		{"extra requirement", "History retained.Publisher", `[]module.ID{"history","extra"}`, "", "must match"},
		{"platform import", "History retained.Publisher", `[]module.ID{"history"}`, "platform", "must match"},
		{"test import ignored", "History retained.Publisher", `[]module.ID{"history"}`, "test", ""},
		{"repeated requirement", "History retained.Publisher", `[]module.ID{"history","history"}`, "", "repeated requirement"},
		{"computed requirements", "History retained.Publisher", `requiredModules`, "", "Requires must be a literal list"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspaceFixture(t, []string{"./" + owner}, map[string]string{owner: prefix + "/" + owner})
			writeWorkspaceSource(t, filepath.Join(owner, "module.go"), fmt.Sprintf(template, tt.deps, tt.requires))
			if tt.extra != "" {
				file := "extra_test.go"
				tag := ""
				if tt.extra == "platform" {
					file = "extra_plan9.go"
					tag = "//go:build plan9\n\n"
				}
				writeWorkspaceSource(t, filepath.Join(owner, "internal", file), tag+"package internal\nimport _ \"github.com/yusing/mekugi/modules/extra/api\"\n")
			}
			err := checkRequires(member{owner, prefix + "/" + owner})
			if tt.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("error=%v, want %q", err, tt.reason)
			}
		})
	}
}
