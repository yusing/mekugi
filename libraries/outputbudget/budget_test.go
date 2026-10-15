package outputbudget

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func nativeFormatFixture(t *testing.T, budget int, mode, stdout, stderr string) Selection {
	t.Helper()
	output, err := Select(t.Context(), budget, mode, stdout, stderr)
	if err != nil {
		t.Fatal(err)
	}
	return output
}
func nativeTokens(t *testing.T, text string) int {
	t.Helper()
	budget := Budget{}
	count, err := budget.Count(text)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

// Source: internal/router/toolplugin/tests/tokens.test.ts
func TestNativeOutputHeadTailUnicodeAndSharedBudget(t *testing.T) {
	for _, mode := range []string{"head", "tail"} {
		for _, budget := range []int{1, 2, 7, 42, 100} {
			for _, source := range []string{"", "short\n", strings.Repeat("first 🙂 café 中文 👨‍👩‍👧‍👦\n", 30), strings.Repeat(" ", 5000), "<|endoftext|>\r\n", "\ufeffhello world more words", "before\ufeffhello world", "\ufeff\ufeff"} {
				output := nativeFormatFixture(t, budget, mode, source, "")
				if !utf8.ValidString(output.Stdout) || nativeTokens(t, output.Stdout) > budget {
					t.Fatalf("invalid token cut: %+v", output)
				}
				if mode == "head" && !strings.HasPrefix(source, output.Stdout) || mode == "tail" && !strings.HasSuffix(source, output.Stdout) {
					t.Fatalf("selection is not %s: %q", mode, output.Stdout)
				}
			}
		}
	}
	tiny := nativeFormatFixture(t, 1, "tail", "stdout", "first error")
	if tiny.Stdout == "" || tiny.Stderr != "" {
		t.Fatalf("one-token allocation: %+v", tiny)
	}
	for _, budget := range []int{2, 7, 42} {
		output := nativeFormatFixture(t, budget, "head", strings.Repeat("out ", 200), strings.Repeat("err ", 200))
		out, err := nativeTokens(t, output.Stdout), nativeTokens(t, output.Stderr)
		if out == 0 || err > budget/2 || out+err > budget {
			t.Fatalf("shared budget %d: %+v (%d/%d)", budget, output, out, err)
		}
	}
	for _, budget := range []int{0, -1} {
		if _, err := Select(t.Context(), budget, "head", "", ""); err == nil {
			t.Fatal("invalid budget accepted")
		}
	}
	if _, err := Select(t.Context(), 1, "unknown", "", ""); err == nil {
		t.Fatal("invalid selection mode accepted")
	}

}

func TestNativeOutputRows(t *testing.T) {
	source := strings.Repeat("\"path\\\\name\":1:abcd \"\t🙂 café 中文\"\r\n", 500)
	for _, budget := range []int{1, 20, 123, 256, 1600, 8976} {
		rows := nativeFormatFixture(t, budget, "rows", source, "")
		if !strings.HasPrefix(source, rows.Stdout) || rows.Stdout != "" && !strings.HasSuffix(rows.Stdout, "\n") || nativeTokens(t, rows.Stdout) > budget {
			t.Fatalf("row cut: %+v", rows)
		}
	}
}

func TestSelectionNeedsNoResolverRuntime(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	result, err := Select(t.Context(), 3, "head", "hello world", "diagnostic")
	if err != nil || result.Stdout != "hello world" || result.Stderr != "diagn" {
		t.Fatalf("isolated selection = %+v, %v", result, err)
	}
}
