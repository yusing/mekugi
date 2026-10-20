package read

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/command"
)

// Keep the reference deliberately greedy: token counts are not additive across
// row boundaries, particularly around punctuation, whitespace, and Unicode.
func referenceNativeRowSelection(t *testing.T, rows []string, budget, limit int, tail bool) (string, string) {
	t.Helper()
	shown := ""
	selected := 0
	for step := range len(rows) {
		if limit > 0 && selected == limit {
			break
		}
		index := step
		if tail {
			index = len(rows) - 1 - step
		}
		candidate := shown + rows[index]
		if tail {
			candidate = rows[index] + shown
		}
		if nativeTokens(t, candidate) > budget {
			break
		}
		shown = candidate
		selected++
	}
	all := strings.Join(rows, "")
	if tail {
		return shown, strings.TrimSuffix(all, shown)
	}
	return shown, strings.TrimPrefix(all, shown)
}

func assertNativeRowBudget(t *testing.T, out command.Output, shown, omitted string, budget int) {
	t.Helper()
	if out.Stdout != shown {
		t.Fatalf("shown rows differ: got %q, want %q", out.Stdout, shown)
	}
	if !utf8.ValidString(out.Stdout) || nativeTokens(t, out.Stdout) > budget {
		t.Fatalf("invalid UTF-8 or token cap exceeded: budget %d, stdout %q", budget, out.Stdout)
	}
	if omitted == "" {
		if out.OmittedOutput != nil {
			t.Fatalf("unexpected omitted output: %+v", out.OmittedOutput)
		}
		return
	}
	if out.ExitCode != 1 || out.FailureClass != "output_limit" || out.OmittedOutput == nil {
		t.Fatalf("missing output-limit recovery: %+v", out)
	}
	if out.OmittedOutput.Stdout != omitted || !utf8.ValidString(out.OmittedOutput.Stdout) {
		t.Fatalf("omitted rows differ: got %q, want %q", out.OmittedOutput.Stdout, omitted)
	}
}

func TestNativeMCatRowBudgetGreedyReference(t *testing.T) {
	logical := []string{
		"alpha", "", "!!!", ");", "中文 日本語 🧪", " ",
		"x := []string{", "\"quoted\",", "}", "café e\u0301", "", "final?!",
	}
	// Mixed terminators and an unterminated final row must produce logical LF
	// rows, without losing blank rows or splitting a UTF-8 rune.
	var source strings.Builder
	for i, row := range logical {
		source.WriteString(row)
		if i != len(logical)-1 {
			source.WriteString([]string{"\r\n", "\n", "\r"}[i%3])
		}
	}
	path := nativeFixture(t, "varied.txt", source.String())
	for _, numbered := range []bool{false, true} {
		for _, tail := range []bool{false, true} {
			for _, limit := range []int{0, 1, 4} {
				for _, ranged := range []bool{false, true} {
					name := fmt.Sprintf("number=%t/tail=%t/limit=%d/range=%t", numbered, tail, limit, ranged)
					t.Run(name, func(t *testing.T) {
						start, end := 0, len(logical)
						if ranged {
							start, end = 1, len(logical)-1
						}
						var rows []string
						for i := start; i < end; i++ {
							row := logical[i] + "\n"
							if numbered {
								row = fmt.Sprintf("%6d\t%s", i+1, row)
							}
							rows = append(rows, row)
						}
						boundary := nativeTokens(t, strings.Join(rows[:3], ""))
						if tail {
							boundary = nativeTokens(t, strings.Join(rows[len(rows)-3:], ""))
						}
						for _, budget := range []int{1, 2, 3, 7, boundary - 1, boundary, boundary + 1, nativeTokens(t, strings.Join(rows, ""))} {
							t.Run(strconv.Itoa(budget), func(t *testing.T) {
								args := []string{"--max-tokens", strconv.Itoa(budget)}
								if numbered {
									args = append(args, "--number")
								}
								if tail {
									args = append(args, "--tail")
								}
								if limit > 0 {
									args = append(args, "-n", strconv.Itoa(limit))
								}
								args = append(args, path)
								if ranged {
									args = append(args, fmt.Sprintf("%d:%d", start+1, end))
								}
								shown, omitted := referenceNativeRowSelection(t, rows, budget, limit, tail)
								out := nativeExecute(t, "mcat", args...)
								assertNativeRowBudget(t, out, shown, omitted, budget)
								if omitted == "" && (out.ExitCode != 0 || out.Stderr != "") {
									t.Fatalf("complete read failed: %+v", out)
								}
								if omitted != "" && out.OmittedOutput.StdoutKind != "rows" {
									t.Fatalf("recovery must preserve row semantics: %+v", out.OmittedOutput)
								}
								all := out.Stdout + omitted
								if tail {
									all = omitted + out.Stdout
								}
								if all != strings.Join(rows, "") {
									t.Fatalf("recovery changed source row order: %q", all)
								}
							})
						}
					})
				}
			}
		}
	}
}
