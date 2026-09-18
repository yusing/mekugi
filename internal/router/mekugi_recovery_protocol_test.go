package router

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func recoverScriptForTest(ctx context.Context, rejectedScript, payload string) (string, error) {
	recovered, err := recoverScriptDetailed(ctx, rejectedScript, payload, testRecoveryHandles(rejectedScript))
	return recovered.script, err
}

func TestMekugiRecoveryDescriptionIsNonInstructional(t *testing.T) {
	const want = "Correction of the latest rejected HPATCH/2 script. Invalid correction leaves the retained script and workspace unchanged."
	if mekugiRecoveryDescription != want {
		t.Fatalf("mekugiRecoveryDescription = %q, want %q", mekugiRecoveryDescription, want)
	}
}

func testRecoveryHandles(script string) []string {
	handles := make([]string, len(recoveryCommands(script, nil)))
	for index := range handles {
		handles[index] = shortHandle(uint64(index))
	}
	return handles
}

func TestRecoveryCommandsBindCompleteFramesAndBaseline(t *testing.T) {
	script := "in file.go\ntype 1:ffff <<PATCH\nfirst\nsecond\nPATCH\n"
	handles := testRecoveryHandles(script)
	commands := recoveryCommands(script, handles)
	if len(commands) != 2 || commands[1].source != "type 1:ffff <<PATCH\nfirst\nsecond\nPATCH\n" {
		t.Fatalf("commands = %+v", commands)
	}
	binding := recoveryHandlesBinding(script, handles)
	for _, changed := range []string{
		strings.Replace(script, "second", "changed", 1),
		strings.Replace(script, "file.go", "other.go", 1),
	} {
		if recoveryHandlesBinding(changed, handles) == binding {
			t.Fatal("changed body or file context retained the old binding")
		}
	}
}

func TestRecoveryHandlesRejectLegacyAndNoncanonicalEncoding(t *testing.T) {
	commands := recoveryCommands("in file.go\ntype 1:ffff \"new\"\n", []string{"amber", "apple"})
	for _, invalid := range []string{"C2:" + strings.Repeat("A", 43), "apple0", "apple01", "Apple", "apple="} {
		if _, err := resolveRecoveryCommand(commands, invalid); err == nil || !strings.Contains(err.Error(), "invalid command handle") {
			t.Fatalf("noncanonical handle %q: %v", invalid, err)
		}
	}
	if _, err := resolveRecoveryCommand(commands, "apple"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRecoveryCommand(commands, "maple"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("unavailable handle: %v", err)
	}
}

func TestRecoverScriptRetargetsBatchAndPreservesOtherFields(t *testing.T) {
	script := "in first.go\n" +
		"add 1:aaaa <<PATCH\n" +
		"first\n" +
		"second\n" +
		"PATCH\n" +
		"in second.go\n" +
		`type "old text" "new text"` + "\n"
	commands := recoveryCommands(script, testRecoveryHandles(script))
	if !commands[1].parts.parsed || !commands[3].parts.parsed {
		t.Fatalf("command parts = %+v and %+v", commands[1].parts, commands[3].parts)
	}
	payload := strings.Join([]string{
		commands[1].handle + " 2:bbbb",
		commands[3].handle + ` "current text" 2`,
	}, "\n")
	recovered, err := recoverScriptDetailed(t.Context(), script, payload, testRecoveryHandles(script))
	if err != nil {
		t.Fatal(err)
	}
	want := "in first.go\n" +
		"add 2:bbbb <<PATCH\n" +
		"first\n" +
		"second\n" +
		"PATCH\n" +
		"in second.go\n" +
		`type "current text" 2 "new text"` + "\n"
	if recovered.script != want {
		t.Fatalf("recovered script = %q, want %q", recovered.script, want)
	}
	wantDelta := strings.Join([]string{
		commands[1].handle + ": 1:aaaa -> 2:bbbb",
		commands[3].handle + `: "old text" -> "current text" 2`,
	}, "\n")
	if recovered.delta != wantDelta {
		t.Fatalf("recovery delta = %q, want %q", recovered.delta, wantDelta)
	}
}

func TestRecoverScriptPreservesHeredocFraming(t *testing.T) {
	for _, marker := range []string{"<<PATCH", "<<'PATCH'", "<<-PATCH"} {
		for _, body := range []string{"", "\n", "first\r\nsecond \t\n", "first\n\n"} {
			for _, finalTerminator := range []string{"", "\n", "\r\n"} {
				script := "in file.txt\ntype 1:aaaa " + marker + "\r\n" + body + "PATCH" + finalTerminator
				command := recoveryCommands(script, testRecoveryHandles(script))[1]
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

func TestRecoverScriptPreservesTextFraming(t *testing.T) {
	for _, marker := range []string{"<<TEXT", "<<'TEXT'"} {
		script := "in file.txt\ntype 1:aaaa " + marker + "\r\n|type <<PATCH\n|PATCH\r\n||body\n|TEXT\nTEXT\n"
		commands := recoveryCommands(script, testRecoveryHandles(script))
		if len(commands) != 2 || !commands[1].parts.parsed {
			t.Fatalf("commands = %+v", commands)
		}
		got, err := recoverScriptForTest(t.Context(), script, commands[1].handle+` "current"`)
		want := strings.Replace(script, "1:aaaa", `"current"`, 1)
		if err != nil || got != want {
			t.Fatalf("recover = %q, %v; want %q", got, err, want)
		}
	}
}

func TestRecoverScriptRetargetsObservedBatchSize(t *testing.T) {
	var script strings.Builder
	var want strings.Builder
	for index := 1; index <= 15; index++ {
		fmt.Fprintf(&script, "in file-%02d.txt\ntype %d:aaaa \"value-%02d\"\n", index, index, index)
		fmt.Fprintf(&want, "in file-%02d.txt\ntype %d:%04x \"value-%02d\"\n", index, index+20, index, index)
	}
	commands := recoveryCommands(script.String(), testRecoveryHandles(script.String()))
	corrections := make([]string, 0, 15)
	for index := range 15 {
		corrections = append(
			corrections,
			fmt.Sprintf("%s %d:%04x", commands[index*2+1].handle, index+21, index+1),
		)
	}
	got, err := recoverScriptForTest(t.Context(), script.String(), strings.Join(corrections, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want.String() {
		t.Fatalf("recovered 15-target script = %q, want %q", got, want.String())
	}
}

func TestRecoverScriptRetargetsMultilineLiteral(t *testing.T) {
	script := "in file.go\n" + `type 2:bbbb "old\ntext" "new text"` + "\n"
	command := recoveryCommands(script, testRecoveryHandles(script))[1]
	got, err := recoverScriptForTest(t.Context(), script, command.handle+` "current\u000Atext"`)
	if err != nil {
		t.Fatal(err)
	}
	want := "in file.go\n" + `type "current\u000Atext" "new text"` + "\n"
	if got != want {
		t.Fatalf("recoverScript() = %q, want %q", got, want)
	}
}

func TestRecoverScriptRejectsNonTargetDuplicateStaleAndMalformedPayloads(t *testing.T) {
	script := "in file.go\n" + `type 1:aaaa "bad"` + "\n"
	commands := recoveryCommands(script, testRecoveryHandles(script))
	handle := commands[1].handle
	for _, payload := range []string{
		commands[0].handle + " 2:bbbb",
		"C2:ffff 2:bbbb",
		handle + " 2:bbbb\n" + handle + " 3:cccc",
		handle + " not-a-target",
		handle + " 2:bbbb trailing",
		handle + "  2:bbbb",
		handle + "\t2:bbbb",
		handle + ` 2:bbbb  "literal"`,
		handle + ` 2:bbbb..3:cccc "literal"`,
		handle + ` "literal"  2`,
		handle + ` "literal"2`,
		handle + " EOF",
		handle + " 1:aaaa",
		"",
	} {
		if got, err := recoverScriptForTest(t.Context(), script, payload); err == nil || got != "" {
			t.Fatalf("recoverScript(%q) = %q, %v; want atomic rejection", payload, got, err)
		}
	}
}

func TestRecoveryCommandPartsPreserveEOFDestination(t *testing.T) {
	command := recoveryCommands(`add EOF "value"`, testRecoveryHandles(`add EOF "value"`))[0]
	if !command.parts.parsed || command.parts.target != "EOF" || command.parts.value != "value" {
		t.Fatalf("command parts = %+v", command.parts)
	}
	if got, err := recoverScriptForTest(t.Context(), command.source, command.handle+" 1:aaaa"); err == nil || got != "" {
		t.Fatalf("EOF recovery = %q, %v; want rejection", got, err)
	}
}

func TestRecoverScriptRejectsSemanticallyUnchangedTarget(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		correction string
	}{
		{
			name:       "single-row range",
			command:    `type 1:aaaa "bad"`,
			correction: "1:aaaa..1:aaaa",
		},
		{
			name:       "unanchored escaped literal and default count",
			command:    `type "old\u000Atext" 1 "bad"`,
			correction: `"old\ntext"`,
		},
		{
			name:       "anchored escaped literal and default count",
			command:    `type 1:aaaa "old\u000Atext" 1 "bad"`,
			correction: `1:aaaa "old\ntext"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := "in file.go\n" + test.command + "\n"
			command := recoveryCommands(script, testRecoveryHandles(script))[1]
			got, err := recoverScriptForTest(t.Context(), script, command.handle+" "+test.correction)
			if err == nil || got != "" || !strings.Contains(err.Error(), "replacement target must differ") {
				t.Fatalf("unchanged recovery = %q, %v", got, err)
			}
		})
	}
}

func TestRecoverScriptAllowsDifferentLiteralOccurrence(t *testing.T) {
	script := "in file.go\n" + `type "old" "bad"` + "\n"
	command := recoveryCommands(script, testRecoveryHandles(script))[1]
	got, err := recoverScriptForTest(t.Context(), script, command.handle+` "old" 2`)
	if err != nil {
		t.Fatal(err)
	}
	want := "in file.go\n" + `type "old" 2 "bad"` + "\n"
	if got != want {
		t.Fatalf("different occurrence recovery = %q, want %q", got, want)
	}
}

func TestRecoverScriptHonorsContext(t *testing.T) {
	script := "in file.go\n" + `type 1:aaaa "bad"` + "\n"
	payload := recoveryCommands(script, testRecoveryHandles(script))[1].handle + " 2:bbbb"
	if got, err := recoverScriptForTest(nil, script, payload); err == nil || got != "" {
		t.Fatalf("nil context = %q, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := recoverScriptForTest(ctx, script, payload); err == nil || got != "" {
		t.Fatalf("cancelled context = %q, %v", got, err)
	}
}

func TestRecoveryGrammarContainsHandleAndOrdinaryTarget(t *testing.T) {
	for _, want := range []string{
		`start: _blank_line* (corrections | mutations) _blank_line*`,
		`recovery: HANDLE SP target`,
		`HANDLE: /[a-z]+[0-9]*/`,
	} {
		if !strings.Contains(mekugiRecoveryGrammar, want) {
			t.Fatalf("recovery grammar does not contain %q", want)
		}
	}
}

func TestRecoveryGrammarMirrorsPublicMultilineTargetTerminal(t *testing.T) {
	for _, name := range []string{"TARGET_QUOTED", "QUOTED", "HEREDOC_MARKER", "HEREDOC_BODY_LINE", "HEREDOC_END"} {
		public := grammarTerminalLine(t, mekugi.ToolGrammar(), name)
		recovery := grammarTerminalLine(t, mekugiRecoveryGrammar, name)
		if recovery != public {
			t.Fatalf("recovery %s = %q, public = %q", name, recovery, public)
		}
	}
}

func grammarTerminalLine(t *testing.T, grammar, name string) string {
	t.Helper()
	prefix := name + ": "
	for line := range strings.SplitSeq(grammar, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("%s terminal not found", name)
	return ""
}
