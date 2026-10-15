package read

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func nativeFixture(t *testing.T, name, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nativeExecute(t *testing.T, name string, args ...string) command.Output {
	t.Helper()
	output, err := (Service{}).Execute(t.Context(), command.Request{Args: args, Stdin: nil})
	if err != nil {
		t.Fatalf("%s %q: %v", name, args, err)
	}
	return output
}

func nativeTokens(t *testing.T, source string) int {
	t.Helper()
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	count, err := codec.Count(source)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

// Source: internal/router/toolplugin/tests/mcat-ranges.test.ts
func TestNativeMCatLogicalRowsAndRanges(t *testing.T) {
	path := nativeFixture(t, "rows.txt", "\ufefffirst\r\n\rthird\nfourth")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"whole", []string{path}, "\ufefffirst\n\nthird\nfourth\n"},
		{"colon", []string{path, "2:3"}, "\nthird\n"},
		{"dash", []string{path, "2-3"}, "\nthird\n"},
		{"zero and EOF clamp", []string{path, "0:99"}, "\ufefffirst\n\nthird\nfourth\n"},
		{"number blank rows", []string{path, "2:3", "--number"}, "     2\t\n     3\tthird\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := nativeExecute(t, "mcat", tc.args...)
			if out.ExitCode != 0 || out.Stderr != "" || out.Stdout != tc.want {
				t.Fatalf("got %+v, want stdout %q", out, tc.want)
			}
		})
	}
	for _, source := range []string{"", "\n", "\r\n", "a\n"} {
		out := nativeExecute(t, "mcat", nativeFixture(t, "empty.txt", source))
		want := strings.ReplaceAll(source, "\r\n", "\n")
		if out.ExitCode != 0 || out.Stdout != want {
			t.Fatalf("source %q: %+v", source, out)
		}
	}
	out := nativeExecute(t, "mcat", path, "5:9")
	if out.ExitCode != 1 || out.FailureClass != "reader_error" || !strings.Contains(out.Stderr, "rows 5:9 past EOF (4 rows)") {
		t.Fatalf("EOF failure: %+v", out)
	}
}

func TestNativeMCatHeadTailAndRetainedNumbering(t *testing.T) {
	path := nativeFixture(t, "rows.txt", "first\r\n\r\nthird")
	for _, tc := range []struct {
		name           string
		args           []string
		shown, omitted string
	}{
		{"head range", []string{"--number", "-n", "1", path, "2:3"}, "     2\t\n", "     3\tthird\n"},
		{"tail", []string{"--number", "--tail", "-n", "1", path}, "     3\tthird\n", "     1\tfirst\n     2\t\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := nativeExecute(t, "mcat", tc.args...)
			if out.ExitCode != 1 || out.Stdout != tc.shown || out.OmittedOutput == nil || out.OmittedOutput.Stdout != tc.omitted || out.OmittedOutput.StdoutKind != "rows" {
				t.Fatalf("bounded numbered output: %+v", out)
			}
		})
	}
}

func TestNativeMCatBudgetAndWholeFileUTF8Validation(t *testing.T) {
	first := "one\n"
	last := strings.Repeat("long final row ", 100) + "\n"
	path := nativeFixture(t, "budget.txt", first+last)
	budget := strconv.Itoa(nativeTokens(t, first))
	out := nativeExecute(t, "mcat", "--max-tokens="+budget, path)
	if out.ExitCode != 1 || out.Stdout != first || out.OmittedOutput == nil || out.OmittedOutput.Stdout != last {
		t.Fatalf("row budget: %+v", out)
	}
	out = nativeExecute(t, "mcat", "--tail", "--max-tokens", budget, path)
	if out.ExitCode != 1 || out.Stdout != "" || out.OmittedOutput == nil || out.OmittedOutput.Stdout != first+last {
		t.Fatalf("tail skipped oversized final row: %+v", out)
	}
	invalid := nativeFixture(t, "invalid.txt", "one\n"+strings.Repeat("x\n", 100)+"\xff")
	for _, args := range [][]string{{"-n", "1", invalid}, {"--tail", "-n", "1", invalid}, {invalid, "1:1"}, {"--max-tokens", "1", invalid}} {
		out = nativeExecute(t, "mcat", args...)
		if out.ExitCode != 1 || !strings.Contains(strings.ToLower(out.Stderr), "utf") {
			t.Fatalf("invalid UTF-8 escaped validation: %+v", out)
		}
	}
}

func TestNativeFrontendInvalidArgumentsAndReadFailures(t *testing.T) {
	path := nativeFixture(t, "valid.go", "package p\n")
	for _, name := range []string{"mcat"} {
		t.Run(name, func(t *testing.T) {
			for _, args := range [][]string{{}, {"--max-tokens", "0", path}, {"--max-tokens", "15501", path}, {"--max-tokens", "1", "--max-tokens=2", path}} {
				out := nativeExecute(t, name, args...)
				if out.ExitCode != 1 || out.Stdout != "" || out.Stderr == "" {
					t.Fatalf("invalid args %q: %+v", args, out)
				}
			}
			for _, target := range []string{filepath.Join(t.TempDir(), "missing \"quoted\".go"), t.TempDir(), nativeFixture(t, "invalid.go", "\xff")} {
				out := nativeExecute(t, name, target)
				if out.ExitCode != 1 || out.Stderr == "" {
					t.Fatalf("read failure %q: %+v", target, out)
				}
			}
		})
	}
}
