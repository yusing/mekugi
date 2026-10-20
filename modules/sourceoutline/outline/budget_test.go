package outline

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func referenceOutlineRows(t *testing.T, rows []string, budget int) (string, string) {
	t.Helper()
	shown := ""
	for _, row := range rows {
		if nativeTokens(t, shown+row) > budget {
			break
		}
		shown += row
	}
	return shown, strings.TrimPrefix(strings.Join(rows, ""), shown)
}
func TestNativeInspectRowBudgetIndependentTargets(t *testing.T) {
	first := nativeFixture(t, "first.md", "# 中文?!\r\n\r\n## café 🧪\r\n### Last;\r\n")
	last := nativeFixture(t, "last.go", "package p\r\n\r\nfunc Café() {}\r\nfunc Last() {}\r\n")
	missing := filepath.Join(t.TempDir(), "missing.go")
	for _, asJSON := range []bool{false, true} {
		for _, failureIndex := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("json=%t/failure=%d", asJSON, failureIndex), func(t *testing.T) {
				paths := []string{first, last}
				paths = append(paths[:failureIndex], append([]string{missing}, paths[failureIndex:]...)...)
				args := []string{"--max-tokens", "15500"}
				if asJSON {
					args = append(args, "--json")
				}
				args = append(args, paths...)
				full := nativeExecute(t, args...)
				if full.ExitCode != 1 || !strings.Contains(full.Stderr, missing) || full.OmittedOutput != nil {
					t.Fatalf("full mixed-target read failed: %+v", full)
				}
				if !strings.Contains(full.Stdout, "Café") || !strings.Contains(full.Stdout, "中文?!") {
					t.Fatalf("successful targets were suppressed: %+v", full)
				}
				rows := strings.SplitAfter(full.Stdout, "\n")
				rows = rows[:len(rows)-1]
				boundary := nativeTokens(t, rows[0])
				for _, budget := range []int{1, 2, 7, boundary - 1, boundary, boundary + 1, nativeTokens(t, full.Stdout)} {
					t.Run(strconv.Itoa(budget), func(t *testing.T) {
						boundedArgs := append([]string(nil), args...)
						boundedArgs[1] = strconv.Itoa(budget)
						shown, omitted := referenceOutlineRows(t, rows, budget)
						out := nativeExecute(t, boundedArgs...)
						if out.Stdout != shown || nativeTokens(t, out.Stdout) > budget || !utf8.ValidString(out.Stdout) {
							t.Fatalf("invalid bounded output: %+v", out)
						}
						if omitted != "" && (out.OmittedOutput == nil || out.FailureClass != "output_limit" || out.OmittedOutput.Stdout != omitted || !utf8.ValidString(omitted)) {
							t.Fatalf("invalid recovery: %+v", out)
						}
						if omitted == "" && out.OmittedOutput != nil {
							t.Fatalf("unexpected recovery: %+v", out)
						}
						if out.ExitCode != 1 || out.Stderr != full.Stderr {
							t.Fatalf("failed-target evidence changed under a budget: got %+v, want stderr %q", out, full.Stderr)
						}
						if out.Stdout+omitted != full.Stdout {
							t.Fatal("retained output does not reconstruct ordered successful targets")
						}
					})
				}
			})
		}
	}
}
