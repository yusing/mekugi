package router

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func recoverScriptForTest(ctx context.Context, rejectedScript, payload string) (string, error) {
	recovered, err := recoverScriptDetailed(ctx, rejectedScript, payload, testRecoveryHandles(rejectedScript))
	return recovered.script, err
}

func testRecoveryHandles(script string) []string {
	handles := make([]string, len(recoveryCommands(script, nil)))
	for index := range handles {
		handles[index] = shortHandle(uint64(index))
	}
	return handles
}

func TestRecoveryCommandsBindCompleteFrames(t *testing.T) {
	script := "type 1:ffff <<PATCH\nfirst\nsecond\nPATCH\n"
	handles := testRecoveryHandles(script)
	commands := recoveryCommands(script, handles)
	if len(commands) != 1 || commands[0].source != script || !commands[0].parts.parsed ||
		commands[0].parts.target != "1:ffff" {
		t.Fatalf("commands = %+v", commands)
	}
	binding := recoveryHandlesBinding(script, handles)
	for _, changed := range []string{
		strings.Replace(script, "second", "changed", 1),
		strings.Replace(script, "1:ffff", "2:eeee", 1),
	} {
		if recoveryHandlesBinding(changed, handles) == binding {
			t.Fatal("changed body or file context retained the old binding")
		}
	}
}

func TestRecoveryHandlesRejectNoncanonicalEncoding(t *testing.T) {
	commands := recoveryCommands("type 1:ffff \"new\"\n", []string{"amber"})
	for _, invalid := range []string{"C2:" + strings.Repeat("A", 43), "amber0", "Amber", "amber="} {
		if _, err := resolveRecoveryCommand(commands, invalid); err == nil || !strings.Contains(err.Error(), "invalid command handle") {
			t.Fatalf("noncanonical handle %q: %v", invalid, err)
		}
	}
	if _, err := resolveRecoveryCommand(commands, "amber"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverScriptRetargetsExplicitPathCommands(t *testing.T) {
	script := "add 1:aaaa <<PATCH\nfirst\nsecond\nPATCH\n" +
		`type "old text" 2 "new text"` + "\n"
	commands := recoveryCommands(script, testRecoveryHandles(script))
	payload := strings.Join([]string{
		commands[0].handle + " 2:bbbb",
		commands[1].handle + ` "current text" 2`,
	}, "\n")
	recovered, err := recoverScriptDetailed(t.Context(), script, payload, testRecoveryHandles(script))
	if err != nil {
		t.Fatal(err)
	}
	want := "add 2:bbbb <<PATCH\nfirst\nsecond\nPATCH\n" +
		`type "current text" 2 "new text"` + "\n"
	if recovered.script != want {
		t.Fatalf("recovered script = %q, want %q", recovered.script, want)
	}
	wantDelta := strings.Join([]string{
		commands[0].handle + ": 1:aaaa -> 2:bbbb",
		commands[1].handle + `: "old text" 2 -> "current text" 2`,
	}, "\n")
	if recovered.delta != wantDelta {
		t.Fatalf("recovery delta = %q, want %q", recovered.delta, wantDelta)
	}
}

func TestRecoverScriptPreservesHeredocFraming(t *testing.T) {
	for _, marker := range []string{"<<PATCH", "<<'PATCH'", "<<-PATCH"} {
		for _, body := range []string{"", "\n", "first\r\nsecond \t\n", "first\n\n"} {
			for _, finalTerminator := range []string{"", "\n", "\r\n"} {
				script := "type 1:aaaa " + marker + "\r\n" + body + "PATCH" + finalTerminator
				command := recoveryCommands(script, testRecoveryHandles(script))[0]
				if !command.parts.parsed {
					t.Fatalf("unparsed command: %+v", command)
				}
				got, err := recoverScriptForTest(t.Context(), script, command.handle+` "current"`)
				want := strings.Replace(script, "1:aaaa", `"current"`, 1)
				if err != nil || got != want {
					t.Fatalf("recover %q = %q, %v; want %q", script, got, err, want)
				}
			}
		}
	}
}

func TestRecoveryCommandValueAndAppend(t *testing.T) {
	script := `append "old"` + "\n"
	command := recoveryCommands(script, testRecoveryHandles(script))[0]
	if !command.parts.parsed || command.parts.value != "old" {
		t.Fatalf("append parts = %+v", command)
	}
	got, err := recoverScriptForTest(t.Context(), script, command.handle+` value "new"`)
	want := `append "new"` + "\n"
	if err != nil || got != want {
		t.Fatalf("append recovery = %q, %v; want %q", got, err, want)
	}
	if got, err := recoverScriptForTest(t.Context(), script, command.handle+" target 1:aaaa"); err == nil || got != "" {
		t.Fatalf("append target recovery = %q, %v; want rejection", got, err)
	}
}

func TestRecoverScriptRetargetsObservedBatchSize(t *testing.T) {
	var script strings.Builder
	var want strings.Builder
	for index := 1; index <= 15; index++ {
		fmt.Fprintf(&script, "type %d:aaaa \"value-%02d\"\n", index, index)
		fmt.Fprintf(&want, "type %d:%04x \"value-%02d\"\n", index+20, index, index)
	}
	commands := recoveryCommands(script.String(), testRecoveryHandles(script.String()))
	corrections := make([]string, 0, 15)
	for index := range 15 {
		corrections = append(corrections, fmt.Sprintf("%s %d:%04x", commands[index].handle, index+21, index+1))
	}
	got, err := recoverScriptForTest(t.Context(), script.String(), strings.Join(corrections, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want.String() {
		t.Fatalf("recovered 15-target script = %q, want %q", got, want.String())
	}
}

func TestRecoverBatchSelectsOneFileAndPreservesOthers(t *testing.T) {
	edits := []mekugi.FileEdit{
		{Path: "first.go", Script: `type 1:aaaa "old"` + "\n"},
		{Path: "second.go", Script: `type 1:bbbb "bad"` + "\n"},
	}
	handles := []string{shortHandle(0), shortHandle(1)}
	recovered, err := recoverBatchDetailed(t.Context(), edits, handles[1]+` "good"`, handles, 2)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.edits[0] != edits[0] || recovered.edits[1].Script != `type "good" "bad"`+"\n" {
		t.Fatalf("recovered edits = %+v", recovered.edits)
	}
	if _, err := recoverBatchDetailed(t.Context(), edits, handles[0]+` "new"`, handles, 0); err == nil || !strings.Contains(err.Error(), "--script") {
		t.Fatalf("ambiguous recovery accepted: %v", err)
	}
	binding := recoveryBatchHandlesBinding(edits, handles)
	if binding == recoveryBatchHandlesBinding([]mekugi.FileEdit{{Path: "first.go", Script: "changed"}}, handles) {
		t.Fatal("batch binding ignored file membership")
	}
}

func TestRecoverBatchMalformedScriptContextAndAggregateBound(t *testing.T) {
	edits := []mekugi.FileEdit{
		{Path: "first.go", Script: `type 1:aaaa "old"` + "\n"},
		{Path: "first.go", Script: "type 1:bbbb \"bad\"\nBROKEN\n"},
	}
	handles := make([]string, len(recoveryBatchCommands(edits, nil)))
	for index := range handles {
		handles[index] = shortHandle(uint64(index))
	}
	guidance := mekugiRecoveryGuidanceBatch(edits, []mekugi.HostRejection{{
		Command: 3, SourceLine: 2, Reason: "script-syntax",
	}}, false, handles)
	if !strings.Contains(guidance, `Script 2, file "first.go" retained-script context:`) ||
		!strings.Contains(guidance, "through hpatch --recover HANDLE --script 2, without file paths") ||
		!strings.Contains(guidance, mekugi.TextReferences(edits[1].Script, 2)) {
		t.Fatalf("malformed second-file guidance = %s", guidance)
	}

	row := strings.Fields(mekugi.TextReferences(edits[1].Script, 2))[0]
	payload := "type " + row + " \"\""
	recovered, err := recoverBatchDetailed(t.Context(), edits, payload, handles, 2)
	if err != nil || recovered.edits[0] != edits[0] || strings.Contains(recovered.edits[1].Script, "BROKEN") {
		t.Fatalf("malformed second-file correction = %+v, %v", recovered, err)
	}

	large := strings.Repeat("x", maxMekugiScriptBytes/2)
	bounded := []mekugi.FileEdit{
		{Path: "first.go", Script: `type 1:aaaa "old"` + "\n"},
		{Path: "second.go", Script: large},
	}
	boundedHandles := make([]string, len(recoveryBatchCommands(bounded, nil)))
	for index := range boundedHandles {
		boundedHandles[index] = shortHandle(uint64(index))
	}
	value := strconv.Quote(strings.Repeat("v", maxMekugiScriptBytes/2))
	_, err = recoverBatchDetailed(t.Context(), bounded, boundedHandles[0]+" value "+value, boundedHandles, 1)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("aggregate recovery bound = %v", err)
	}
}

func TestRecoveryTrailingCountPreservesTargetSelection(t *testing.T) {
	script := `type "old" "new" 2`
	commands := recoveryCommands(script, testRecoveryHandles(script))
	if len(commands) != 1 || !commands[0].parts.parsed || commands[0].parts.target != `"old" 2` {
		t.Fatalf("trailing count was not preserved: %+v", commands)
	}
	recovered, err := recoverScriptForTest(t.Context(), script, commands[0].handle+` value "changed"`)
	if err != nil || recovered != `type "old" 2 "changed"` {
		t.Fatalf("value recovery lost count: %q, %v", recovered, err)
	}
	if _, err := recoverScriptForTest(t.Context(), script, commands[0].handle+` target "old" 2`); err == nil {
		t.Fatal("equivalent target correction was accepted")
	}
}
