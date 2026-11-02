package outline

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourcekind"
)

func TestNativeInspectPreservesLoneSurrogateNames(t *testing.T) {
	for _, source := range []string{`import "\uD800";`, `import '\u{D800}';`} {
		file := nativeFixture(t, "module.ts", source)
		result := nativeExecute(t, "--json", file)
		if result.ExitCode != 0 || !strings.Contains(result.Stdout, `"name":"\ud800"`) || strings.Contains(result.Stdout, "�") {
			t.Fatalf("surrogate import: %+v", result)
		}
	}
	file := nativeFixture(t, "keys.json", `{"\uD800":1,"a\uDFFF":2}`)
	result := nativeExecute(t, "--json", file)
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, `"pointer":"/\ud800"`) || !strings.Contains(result.Stdout, `"pointer":"/a\udfff"`) {
		t.Fatalf("surrogate pointer: %+v", result)
	}
	result = nativeExecute(t, file)
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, `\ud800`) {
		t.Fatalf("compact surrogate pointer: %+v", result)
	}
}

func TestFrontmatterUnicodeColumns(t *testing.T) {
	for _, fields := range []string{`{"é": a,"next": b}`, `{'🧪': a,'next': b}`} {
		t.Run(fields, func(t *testing.T) {
			source := "---\n" + fields + "\n---\n# After\n"
			path := nativeFixture(t, "unicode.md", source)
			result := nativeInspectFixture(t, path)
			if !result.Data.ParseComplete || len(result.Data.Outline) != 3 {
				t.Fatalf("outline: %+v", result.Data)
			}
			entry := result.Data.Outline[1]
			if entry.Name != "next" || entry.Line != 2 || entry.LineEnd != 2 {
				t.Fatalf("later key span: %+v", entry)
			}
			snapshot, err := inspectSourceFixture(t.Context(), path, []byte(source))
			if err != nil || !strings.Contains(snapshot, "2-2 frontmatter next\n") {
				t.Fatalf("snapshot=%q err=%v", snapshot, err)
			}
		})
	}
}

func inspectSourceFixture(ctx context.Context, path string, bytes []byte) (string, error) {
	data := inspectionData{Path: filepath.ToSlash(path), Size: int64(len(bytes)), Complete: true}
	format, ok := sourcekind.Classify(path)
	if !ok || !format.Outline {
		return "(no outline)\n", nil
	}
	data, err := inspectSource(ctx, data, bytes, format)
	if err != nil {
		return "", err
	}
	return strings.Join(compactNativeOutline(data), ""), nil
}
