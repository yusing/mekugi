package router

import (
	"slices"
	"strings"
	"testing"
)

func TestShellInvocationSourceFidelity(t *testing.T) {
	t.Parallel()
	invocation := shellInvocation{CallID: "call-test", JournalToken: "private-token"}
	for _, body := range []string{
		"",
		"printf ok\n",
		"printf ok\r",
		"cat <<'EOF'\nbody\nEOF",
		"printf ok\n# mekugi:invocation={authored trailing comment}",
		"printf 'a\\nb'",
		"#!params={\"max_output_tokens\":100}\r\n\nprintf ok\r\n",
		"printf data | cat",
		"#!python3\nprint('ok')\n",
		"# mekugi:invocation={\"call_id\":\"authored-comment\"}\nprintf ok",
	} {
		got, source, err := parseShellInvocation(invocation.source(body))
		if err != nil || got != invocation || source != body {
			t.Fatalf("framing changed source: %+v %q %v", got, source, err)
		}
	}
	for _, source := range []string{
		shellInvocationPrefix + "{}",
		shellInvocationPrefix + "{broken}\nexit 0",
		shellInvocationPrefix + `{"call_id":"/private/path"}` + "\nexit 0",
	} {
		if _, _, err := parseShellInvocation(source); err == nil {
			t.Fatal("accepted invalid invocation framing")
		}
	}
}

func TestShellInvocationWorkerStripsMetadataAndDoesNotExportIt(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh", "python3"} {
		body := `printf '%s' "${MEKUGI_JOURNAL_TOKEN-unset}/${MEKUGI_AX_CALL_ID-unset}"; exit 7`
		if interpreter == "python3" {
			body = "import os\nprint(os.getenv('MEKUGI_JOURNAL_TOKEN', 'unset') + '/' + os.getenv('MEKUGI_AX_CALL_ID', 'unset'), end='')\nraise SystemExit(7)"
		}
		source := (shellInvocation{CallID: "call-private", JournalToken: "private-token"}).source(body)
		invocation := newShellWorkerTestInvocation(t.TempDir())
		// Do not borrow metadata from an enclosing test or active router session.
		for index, v := range slices.Backward(invocation.environment) {
			if strings.HasPrefix(v, "MEKUGI_JOURNAL_TOKEN=") ||
				strings.HasPrefix(v, "MEKUGI_AX_CALL_ID=") {
				invocation.environment = append(invocation.environment[:index], invocation.environment[index+1:]...)
			}
		}
		stdout, stderr, code := runShellWorkerTest(t, registry, interpreter, nil, source, nil, invocation)
		if code != 7 || stdout != "unset/unset" || stderr != "" {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", interpreter, code, stdout, stderr)
		}
	}
}

func TestShellInvocationCommentaryIsCallLocal(t *testing.T) {
	t.Parallel()
	thread := &threadShellCommentarySink{httpShellCommentarySink{token: "thread-token"}}
	first := (shellInvocation{JournalToken: "first"}).commentary(thread).(*threadShellCommentarySink)
	second := (shellInvocation{JournalToken: "second"}).commentary(thread).(*threadShellCommentarySink)
	if first.token != "first" || second.token != "second" || thread.token != "thread-token" {
		t.Fatal("invocation modified another sink")
	}
	if sink := (shellInvocation{JournalToken: "first"}).commentary(nil); sink != nil {
		t.Fatal("capability bypassed thread discovery")
	}
}

func TestShellInvocationCarrierHasNoPrefixes(t *testing.T) {
	t.Parallel()
	registry := &toolRegistry{}
	contribution := toolContribution{PluginID: builtinToolsPluginID, Name: "shell"}
	for _, template := range []string{"", "command {.}", "printf data | {.} | cat"} {
		command, err := registry.execCarrierCommand(contribution, "printf ok", []string{"bash", "printf ok"}, template, 0, "call-test", "private-token")
		if err != nil {
			t.Fatal(err)
		}
		worker := "shell bash " + shellQuoteArgument((shellInvocation{CallID: "call-test", JournalToken: "private-token"}).source("printf ok"))
		want := worker
		if template != "" {
			want = strings.Replace(template, "{.}", worker, 1)
		}
		if command != want {
			t.Fatalf("unexpected carrier prefix: %q", command)
		}
	}
	for _, body := range []string{"git status", "shell --help", "shell bash 'printf ok'"} {
		direct, err := registry.execCarrierCommand(contribution, body, []string{"bash", body}, "", 0, "call-test", "private-token")
		if err != nil || direct != body {
			t.Fatalf("direct command changed: %q %v", direct, err)
		}
	}
}

func TestShellInvocationInspectionRejectsNonWorkerAndAmbiguousCalls(t *testing.T) {
	t.Parallel()
	source := shellQuoteArgument((shellInvocation{CallID: "call-one"}).source(":"))
	other := shellQuoteArgument((shellInvocation{CallID: "call-two"}).source(":"))
	for _, command := range []string{
		"command -v shell bash " + source,
		"env --split-string=echo shell bash " + source,
		"env -u shell bash " + source,
		"env --help shell bash " + source,
		"echo shell bash " + source,
		"printf '%s' shell bash " + source,
		"shell bash " + source + "; shell bash " + other,
		"shell bash \"$(printf '%s' " + source + ")\"",
	} {
		if got := inspectionAXCallID(mustMarshalJSON(command)); got != "" {
			t.Fatalf("invented attribution %q for %q", got, command)
		}
	}
}

func TestShellInvocationCarrierEscapesAuthoredFramingComment(t *testing.T) {
	t.Parallel()
	body := shellInvocationPrefix + "{authored comment}\nprintf ok"
	registry := &toolRegistry{}
	contribution := toolContribution{PluginID: builtinToolsPluginID, Name: "shell"}
	command, err := registry.execCarrierCommand(contribution, body, []string{"bash", body}, "", 0)
	want := "shell bash " + shellQuoteArgument(shellInvocationPrefix+"{}\n"+body)
	if err != nil || command != want {
		t.Fatalf("authored framing was not escaped: %q %v", command, err)
	}
	invocation, source, err := parseShellInvocation((shellInvocation{}).source(body))
	if err != nil || invocation != (shellInvocation{}) || source != body {
		t.Fatalf("authored comment consumed: %+v %q %v", invocation, source, err)
	}
	stdout, stderr, code := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil,
		(shellInvocation{}).source(body), nil)
	if stdout != "ok" || stderr != "" || code != 0 {
		t.Fatalf("authored comment changed execution: %q %q %d", stdout, stderr, code)
	}
}

func TestShellInvocationInspectionCommandPathOption(t *testing.T) {
	t.Parallel()
	source := shellQuoteArgument((shellInvocation{CallID: "call-one"}).source(":"))
	if got := inspectionAXCallID(mustMarshalJSON("command -p shell bash " + source)); got != "call-one" {
		t.Fatalf("command -p lost invocation identity: %q", got)
	}
}

func TestShellInvocationCommandPreviewStartsWithProgram(t *testing.T) {
	t.Parallel()
	registry := &toolRegistry{}
	contribution := toolContribution{PluginID: builtinToolsPluginID, Name: "shell"}
	body := "printf first\nprintf second"
	command, err := registry.execCarrierCommand(contribution, body, []string{"bash", body}, "", 0, "call-test", "private-token")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(command, "shell bash $'printf first\\nprintf second\\n") {
		t.Fatalf("metadata obscures program preview: %q", command)
	}
	if got := inspectionAXCallID(mustMarshalJSON(command)); got != "call-test" {
		t.Fatalf("trailing metadata lost attribution: %q", got)
	}
}

func TestShellInvocationLegacySource(t *testing.T) {
	t.Parallel()
	want := shellInvocation{CallID: "call-old", JournalToken: "old-token"}
	body := "#!python3\nprint('ok')\n# mekugi:invocation={\"call_id\":\"authored-comment\"}"
	source := shellInvocationPrefix + string(mustMarshalJSON(want)) + "\n" + body
	got, recovered, err := parseShellInvocation(source)
	if err != nil || got != want || recovered != body {
		t.Fatalf("legacy carrier changed: %+v %q %v", got, recovered, err)
	}
	if got := inspectionAXCallID(mustMarshalJSON("shell python3 " + shellQuoteArgument(source))); got != want.CallID {
		t.Fatalf("legacy attribution changed: %q", got)
	}
}
