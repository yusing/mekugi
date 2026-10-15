package router

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

// The checked-in Markdown is generated from the external layout and the
// native built-in tool specifications.
func TestGeneratedFrontendGuidanceIsCurrent(t *testing.T) {
	var entries []struct{ Name, Description string }
	for _, tool := range toolplugin.Builtins() {
		var specification struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(tool.Specification, &specification); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, struct{ Name, Description string }{specification.Name, specification.Description})
	}
	var rendered bytes.Buffer
	tmpl, err := template.ParseFiles(filepath.Join("..", "..", "guidance", "frontend_guidance.md.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tmpl.Execute(&rendered, entries); err != nil {
		t.Fatal(err)
	}
	want := []byte(strings.TrimRight(rendered.String(), "\r\n"))
	if os.Getenv("MEKUGI_UPDATE_FRONTEND_GUIDANCE") == "1" {
		if err := os.WriteFile("frontend_guidance.md", want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile("frontend_guidance.md")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("generated frontend guidance is stale or missing; regenerate it with MEKUGI_UPDATE_FRONTEND_GUIDANCE=1 go test ./internal/router -run '^TestGeneratedFrontendGuidanceIsCurrent$': %v", err)
	}
}
