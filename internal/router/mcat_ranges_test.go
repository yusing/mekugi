package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReadBundleNormalizesColonAndDashSpansPerPath(t *testing.T) {
	specs, _, err := parseReadBundle([]string{"first", "1:3", "4-6", "second", "7-9"})
	if err != nil {
		t.Fatal(err)
	}
	want := []readBundleSpec{
		{path: "first", span: "1:3"},
		{path: "first", span: "4:6"},
		{path: "second", span: "7:9"},
	}
	if len(specs) != len(want) {
		t.Fatalf("spec count = %d, want %d: %+v", len(specs), len(want), specs)
	}
	for i := range want {
		if specs[i] != want[i] {
			t.Fatalf("spec %d = %+v, want %+v", i, specs[i], want[i])
		}
	}
}

func TestParseReadBundleCountsMultipleRangesTowardSixteenReadLimit(t *testing.T) {
	args := []string{"source"}
	for read := 1; read <= 16; read++ {
		args = append(args, fmt.Sprintf("%d-%d", read, read))
	}
	specs, _, err := parseReadBundle(args)
	if err != nil || len(specs) != 16 {
		t.Fatalf("16 ranges for one path: specs=%+v err=%v", specs, err)
	}
	args = append(args, "17:17")
	if _, _, err := parseReadBundle(args); err == nil {
		t.Fatal("accepted 17 reads represented as ranges on one path")
	}
}

func TestReadBundleRejectsMalformedRangesBeforeReadingAndSuggestsCorrections(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "rows"), []byte("must not be returned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := sharedProxyTestRegistry(t)
	for _, test := range []struct {
		command string
		want    string
	}{
		{"mcat rows 4:+2", `mcat: invalid range "4:+2"; retry: mcat rows 4:5`},
		{"mcat rows 4,6", `mcat: invalid range "4,6"; retry: mcat rows 4:6`},
		{"mcat rows 4", `mcat: invalid range "4"; retry: mcat rows 4:4`},
		{"mcat -n 4:9 rows", "mcat: -n takes a row count, not a range; retry: mcat PATH 4:9"},
	} {
		out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil, test.command, nil,
			newShellWorkerTestInvocation(directory))
		if status != 1 || out != "" || !strings.Contains(diagnostic, test.want) {
			t.Fatalf("%s: status=%d stdout=%q stderr=%q, want rejection %q before a source read", test.command, status, out, diagnostic, test.want)
		}
	}
}

func TestReadBundleColonPathDisambiguation(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	if err := os.WriteFile(source, []byte("source content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operand := source + ":2"
	_, _, err := parseReadBundle([]string{operand})
	want := "ranges must be separate operands; retry: " + workerCommand("mcat", []string{source, "2:2"})
	if err == nil || err.Error() != want {
		t.Fatalf("nonexistent PATH:N error = %v, want %q", err, want)
	}
	if err := os.WriteFile(operand, []byte("literal colon path\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	specs, _, err := parseReadBundle([]string{operand})
	if err != nil || len(specs) != 1 || specs[0] != (readBundleSpec{path: operand}) {
		t.Fatalf("existing literal PATH:N was parsed as a range: specs=%+v err=%v", specs, err)
	}
	registry := sharedProxyTestRegistry(t)
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		workerCommand("mcat", []string{operand}), nil, newShellWorkerTestInvocation(directory))
	if status != 0 || diagnostic != "" || out != "literal colon path\n" {
		t.Fatalf("existing literal path: status=%d stdout=%q stderr=%q", status, out, diagnostic)
	}
}

func TestMCatDashFilenameAndEOFRangeMessages(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "100-150"), []byte("literal range-like filename\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "rows"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := sharedProxyTestRegistry(t)
	out, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
		"mcat ./100-150", nil, newShellWorkerTestInvocation(directory))
	if status != 0 || diagnostic != "" || out != "literal range-like filename\n" {
		t.Fatalf("range-like filename: status=%d stdout=%q stderr=%q", status, out, diagnostic)
	}

	out, diagnostic, status = runShellWorkerTest(t, registry, "bash", nil,
		"mcat rows 1:9", nil, newShellWorkerTestInvocation(directory))
	if status != 0 || out != "one\ntwo\nthree\n" || diagnostic != "mcat: rows 4:9 past EOF (3 rows)\n" {
		t.Fatalf("partial EOF range: status=%d stdout=%q stderr=%q", status, out, diagnostic)
	}
	out, diagnostic, status = runShellWorkerTest(t, registry, "bash", nil,
		"mcat rows 4:9", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || out != "" || diagnostic != "mcat: rows 4:9 past EOF (3 rows)\n" {
		t.Fatalf("range starting past EOF: status=%d stdout=%q stderr=%q", status, out, diagnostic)
	}
}
