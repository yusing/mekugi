package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestMekugiRecoveryGuidanceListsOnlyRowStaleTargetCommands(t *testing.T) {
	script := "in file.go\n" +
		"type 13:974b..16:d10b <<PATCH\n" +
		"replacement\n" +
		"broken\n" +
		"PATCH\n"
	rejections := []mekugi.HostRejection{{
		Command: 2, SourceLine: 2, Operation: "type", Target: "range", Reason: "row-stale",
	}}
	guidance := mekugiRecoveryGuidance(script, rejections, true, testRecoveryHandles(script))
	command := recoveryCommands(script, testRecoveryHandles(script))[1]
	for _, want := range []string{
		"Rejected target commands:",
		command.handle,
		"This re-rejection changed no workspace file",
		"HANDLE CURRENT_TARGET",
		"preserves every operation and value",
	} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("guidance does not contain %q:\n%s", want, guidance)
		}
	}
	for _, absent := range []string{
		recoveryCommands(script, testRecoveryHandles(script))[0].handle,
		"replacement",
		"broken",
	} {
		if strings.Contains(guidance, absent) {
			t.Fatalf("guidance contains unrelated %q:\n%s", absent, guidance)
		}
	}
}

func TestMekugiRecoveryGuidanceOffersScriptEditsForNonTargetFailure(t *testing.T) {
	script := "in file.go\n" + `type 1:abcd "sensitive replacement" trailing` + "\n"
	guidance := mekugiRecoveryGuidance(
		script,
		[]mekugi.HostRejection{{Command: 2, SourceLine: 2, Operation: "type", Reason: "language-syntax"}},
		false, testRecoveryHandles(script),
	)
	if !strings.Contains(guidance, "Retained rejected-script rows:") ||
		!strings.Contains(guidance, "ordinary type/add mutations") ||
		!strings.Contains(guidance, "not workspace files") ||
		!strings.Contains(guidance, mekugi.TextReferences(script, 2)) {
		t.Fatalf("non-target guidance = %q", guidance)
	}
}

func TestGenericRecoveryPreviewUsesBoundedScriptRows(t *testing.T) {
	script := "\nnew file.go\r\ntype <<TEXT\r\n|package p\r\n|var =\r\nTEXT\r\n"
	rejections := []mekugi.HostRejection{{Command: 2, SourceLine: 3, ValueLine: 2, Reason: "language-syntax"}}
	guidance := genericRecoveryGuidance(script, rejections, true, testRecoveryHandles(script))
	if !strings.Contains(guidance, mekugi.TextReferences(script, 5)) ||
		!strings.Contains(guidance, "Corrections are retained only in the new rejected-script baseline") {
		t.Fatalf("missing script-row identity or baseline warning:\n%s", guidance)
	}
	var many strings.Builder
	var failures []mekugi.HostRejection
	for index := range 30 {
		many.WriteString("rm\n")
		failures = append(failures, mekugi.HostRejection{Command: index + 1, SourceLine: index + 1, Reason: "active-file"})
	}
	bounded := genericRecoveryGuidance(many.String(), failures, false, testRecoveryHandles(many.String()))
	if !strings.Contains(bounded, "Preview limited to 12 script rows.") || strings.Contains(bounded, mekugi.TextReferences(many.String(), 13)) {
		t.Fatalf("unbounded script preview: %s", bounded)
	}
}
