package read

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestBundleDeliveryUsesActualReferencesAndCapturedRows(t *testing.T) {
	const budget = 800
	var source strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&source, "source row %03d alpha beta gamma delta\n", i)
	}
	paths := []string{nativeFixture(t, "first.txt", source.String()), nativeFixture(t, "second.txt", source.String())}
	args := []string{"--number", "--max-tokens", "800", paths[0], "5:100", paths[1], "3:22"}
	out, err := Execute(t.Context(), args, nil)
	if err != nil || out.Delivery == nil {
		t.Fatalf("bundle=%+v err=%v", out, err)
	}
	plan := out.Delivery
	if plan.ReferenceCount() != 2 {
		t.Fatalf("reference slots=%d, want 2", plan.ReferenceCount())
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := plan.Prepare([]string{"one"}); err == nil {
		t.Fatal("accepted mismatched reference count")
	}
	short, err := plan.Prepare([]string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(short.Recovery) != 1 || short.Recovery[0].ReferenceIndex != 0 {
		t.Fatalf("short framing should leave second source complete: %+v", short)
	}
	references := []string{strings.Repeat("first_handle_", 60), strings.Repeat("second_handle_", 60)}
	long, err := plan.Prepare(references)
	if err != nil {
		t.Fatal(err)
	}
	if len(long.Recovery) != 2 {
		t.Fatalf("long framing must retain newly omitted second source: %+v", long)
	}
	if long.Unreferenced.Stdout == short.Unreferenced.Stdout {
		t.Fatal("actual handle widths did not affect admission")
	}
	for _, prepared := range []command.PreparedDelivery{short, long} {
		if used := nativeTokens(t, prepared.Referenced.Stdout); used > budget {
			t.Fatalf("framed tokens=%d, budget=%d", used, budget)
		}
		if prepared.Referenced.ExitCode != 1 || prepared.Referenced.FailureClass != "output_limit" {
			t.Fatalf("incomplete status=%+v", prepared.Referenced)
		}
		continuation := strings.Index(prepared.Referenced.Stdout, "\nnext_call: mread ")
		if continuation < 0 {
			t.Fatal("missing combined recovery command")
		}
		fallback := prepared.Referenced
		fallback.Stdout = fallback.Stdout[:continuation]
		if !reflect.DeepEqual(fallback, prepared.Unreferenced) {
			t.Fatal("fallback changed admitted bodies, diagnostics, or status")
		}
		if fallback.Delivery != nil || fallback.OmittedOutput != nil {
			t.Fatal("prepared output contains unfinished delivery")
		}
		recoveries := make(map[int]command.RecoveryOutput)
		for _, recovery := range prepared.Recovery {
			if recovery.ReferenceIndex < 0 || recovery.ReferenceIndex >= len(paths) {
				t.Fatalf("invalid recovery slot=%d", recovery.ReferenceIndex)
			}
			if _, exists := recoveries[recovery.ReferenceIndex]; exists {
				t.Fatal("duplicate recovery slot")
			}
			recoveries[recovery.ReferenceIndex] = recovery
		}
		for index, bounds := range [][2]int{{5, 100}, {3, 22}} {
			shown := bundleBody(t, prepared.Unreferenced.Stdout, index+1)
			recovery := recoveries[index]
			var want strings.Builder
			for row := bounds[0]; row <= bounds[1]; row++ {
				fmt.Fprintf(&want, "%6d\tsource row %03d alpha beta gamma delta\n", row, row)
			}
			if shown+recovery.Stdout != want.String() {
				t.Fatalf("source %d not reconstructed in selected row order", index+1)
			}
			if recovery.Stdout != "" && (recovery.SourceRow != uint64(bounds[0]+strings.Count(shown, "\n")) || recovery.StdoutKind != "rows" || recovery.ExitCode != 1) {
				t.Fatalf("source %d recovery metadata=%+v", index+1, recovery)
			}
		}
	}
	wantContinuation := "\nnext_call: mread " + strings.Join(references, " ") + " --max-tokens 800\n"
	if !strings.HasSuffix(long.Referenced.Stdout, wantContinuation) {
		t.Fatal("continuation lost actual reference identities or input order")
	}
	again, err := plan.Prepare([]string{"a", "b"})
	if err != nil || !reflect.DeepEqual(again, short) {
		t.Fatalf("Prepare mutated captured plan: err=%v", err)
	}
}

func bundleBody(t *testing.T, output string, index int) string {
	t.Helper()
	header := fmt.Sprintf("--- file %d path=", index)
	start := strings.Index(output, header)
	if start < 0 {
		return ""
	}
	start += strings.IndexByte(output[start:], '\n') + 1
	body := output[start:]
	if end := strings.Index(body, "\n--- file "); end >= 0 {
		body = body[:end]
	}
	return body
}
