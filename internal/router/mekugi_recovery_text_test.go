package router

import (
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestRecoverScriptEditsPreparedText(t *testing.T) {
	const unrelated = "new keep.txt\ntype <<TEXT\n|type <<PATCH\n|PATCH\nTEXT\n"
	for _, test := range []struct {
		name, broken, correction, fixed string
	}{
		{"path", "in wrong.txt\n", `type "wrong.txt" "right.txt"`, "in right.txt\n"},
		{"operation", "wrong command\n", `type "wrong command" "rm"`, "rm\n"},
		{"value", "in f.txt\ntype \"old\" \"bad\"\n", `type "bad" "good"`, "in f.txt\ntype \"old\" \"good\"\n"},
		{"framing", "new f.txt\ntype <<END\nbody\nWRONG\n", `type "WRONG" "END"`, "new f.txt\ntype <<END\nbody\nEND\n"},
		{"missing close", "new f.txt\ntype <<TEXT\n|body\n", `add EOF "TEXT\n"`, "new f.txt\ntype <<TEXT\nbody\nTEXT\n"},
		{"remove conflict", "in f.txt\ntype \"old\" \"new\"\ntype \"old\" \"again\"\n",
			`type "type \"old\" \"again\"\n" ""`, "in f.txt\ntype \"old\" \"new\"\n"},
		{"multiple fields", "in wrong.txt\ntype \"old\" \"bad\"\n",
			"type \"wrong.txt\" \"right.txt\"\ntype \"bad\" \"good\"", "in right.txt\ntype \"old\" \"good\"\n"},
		{"literal protocol value", "new f.txt\ntype \"bad\"\n",
			"type \"type \\\"bad\\\"\\n\" <<'END'\ntype <<PATCH\nvalue\nPATCH\nEND\n", "new f.txt\ntype <<PATCH\nvalue\nPATCH\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A large unrelated prepared value is retained rather than re-emitted.
			prepared := unrelated + "new large.txt\ntype " + strconv.Quote(strings.Repeat("retained ", 10000)) + "\n"
			correction := test.correction
			if test.name == "missing close" {
				row := strings.Fields(mekugi.TextReferences(prepared+test.broken, mekugi.TextLineCount(prepared)+3))[0]
				correction = "type " + row + " " + strconv.Quote("body\nTEXT\n")
			}
			got, err := recoverScriptDetailed(t.Context(), prepared+test.broken, correction, testRecoveryHandles(prepared+test.broken))
			if err != nil || got.script != prepared+test.fixed || got.delta == "" {
				t.Fatalf("recovery error %v; changed unrelated content or failed correction", err)
			}
		})
	}
}

func TestRecoverScriptTextRejectsInvalidOrUnchangedEdits(t *testing.T) {
	const baseline = "new f.txt\ntype \"bad\"\n"
	row := strings.Fields(mekugi.TextReferences(baseline, 2))[0]
	for _, payload := range []string{
		`type "bad" "bad"`,
		`type "missing" "good"`,
		"type 2:ffff \"good\"",
		`type "new f.txt\ntype \"bad\"\n" ""`,
		"type " + row + " \"good\"\ntype " + row + " \"other\"",
		"type \"bad\" \"good\"\nC2:aaaa 1:bbbb",
		"type \"bad\" \"good\"\nrm",
		"type \"bad\" \"good\"\nin other.txt",
	} {
		got, err := recoverScriptDetailed(t.Context(), baseline, payload, testRecoveryHandles(baseline))
		if err == nil || got.script != "" {
			t.Fatalf("accepted invalid correction %q: %v", payload, err)
		}
	}
}

func TestRecoverScriptTextBoundsExpansion(t *testing.T) {
	baseline := "new f.txt\ntype \"xx\"\n"
	payload := `type "x" 2 ` + strconv.Quote(strings.Repeat("v", maxMekugiScriptBytes/2+1))
	got, err := recoverScriptDetailed(t.Context(), baseline, payload, testRecoveryHandles(baseline))
	if err == nil || got.script != "" || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expanded recovery accepted: %d bytes, %v", len(got.script), err)
	}
}
