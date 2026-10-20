package toolplugin

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

// Uses the real repository and installed resolver, with a fresh process per
// iteration but normal OS, Go build, and gopls caches. No resolver is installed.
func BenchmarkNativeSymbol(b *testing.B) {
	if _, err := exec.LookPath("gopls"); err != nil {
		b.Skip("gopls is not on PATH")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		b.Fatal(err)
	}
	const path = "internal/router/session_title_generate.go"
	for _, mode := range []string{"cli", "lsp"} {
		b.Run(mode, func(b *testing.B) {
			file, err := loadSymbolSource(b.Context(), root, path, "", map[string]*symbolSource{})
			if err != nil {
				b.Fatal(err)
			}
			q := &symbolQuery{mode: "refs", path: path, name: "generate", file: file}
			if err := selectNativeSymbol(q); err != nil {
				b.Fatal(err)
			}
			for b.Loop() {
				switch mode {
				case "cli":
					cmd := exec.CommandContext(b.Context(), "gopls", "references", "-d", fmt.Sprintf("%s:#%d", q.file.path, q.offset))
					cmd.Dir = root
					output, err := cmd.CombinedOutput()
					if err != nil || len(output) == 0 {
						b.Fatalf("gopls CLI baseline: %v %s", err, output)
					}
					continue
				case "lsp":
					runNativeLSP(b.Context(), root, "gopls", []*symbolQuery{q})
				}
				if q.err != nil || len(q.locations) == 0 {
					b.Fatalf("resolver: %v %s locations=%d", q.err, q.stderr, len(q.locations))
				}
			}
		})
	}
}
