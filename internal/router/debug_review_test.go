package router

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

func TestDebugAXDiscoveryMatchesMetadataNotThreadSuffix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sessions := filepath.Join(home, "sessions")
	if err := os.Mkdir(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	writeRollout := func(id string) string {
		t.Helper()
		path := filepath.Join(sessions, "rollout-"+id+".jsonl")
		data := mustMarshalJSON(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id}})
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	writeRollout("other-thread")
	found, err := discoverDebugRollouts(t.Context(), []string{"thread"})
	if err != nil || len(found["thread"]) != 0 {
		t.Fatalf("suffix became an exact match: %v, %v", found, err)
	}

	d := featureDebugOutput(t)
	read, err := capturer.StartAXRead(d.paths[4], "thread", "hcat")
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Finish(false); err != nil {
		t.Fatal(err)
	}
	if err := d.writeAXReport(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(d.paths[5])
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		JournalOnly []debugAXThread `json:"journal_only_threads"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.JournalOnly) != 1 || report.JournalOnly[0].State != "rollout_unavailable" || report.JournalOnly[0].Reads.Failed != 1 {
		t.Fatalf("journal-only attribution was invented: %s", data)
	}
	exact := writeRollout("thread")
	found, err = discoverDebugRollouts(t.Context(), []string{"thread", "other-thread"})
	if err != nil || len(found["thread"]) != 1 || found["thread"][0] != exact || len(found["other-thread"]) != 1 {
		t.Fatalf("suffix collision created ambiguity: %v, %v", found, err)
	}
}

func TestAXCarrierTemplatesPreserveWorkerEnvironment(t *testing.T) {
	root := t.TempDir()
	idFile := filepath.Join(root, "identity")
	worker := "#!/bin/sh\n[ \"$#\" -eq 2 ] && [ \"$1\" = bash ] || exit 91\nprintf '%s' \"$MEKUGI_AX_CALL_ID\" > \"$AX_TEST_ID_FILE\"\nexec /bin/bash -c \"$2\"\n"
	if err := os.WriteFile(filepath.Join(root, "shell"), []byte(worker), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AX_TEST_ID_FILE", idFile)
	t.Setenv(capturer.AXCallIDEnvironment, "")
	registry := &toolRegistry{}
	contribution := toolContribution{PluginID: builtinToolsPluginID, Name: "shell"}
	script := "cat; printf result; exit 7"
	for _, tc := range []struct{ template, output string }{
		{"command {.}", "result"},
		{"env {.}", "result"},
		{"printf input | {.} | cat", "inputresult"},
		{":; {.}", "result"},
	} {
		t.Run(tc.template, func(t *testing.T) {
			command, err := registry.execCarrierCommand(contribution, script, []string{"bash", script}, tc.template, 0, "call-review")
			if err != nil {
				t.Fatal(err)
			}
			process := exec.CommandContext(t.Context(), "/bin/bash", "-o", "pipefail", "-c", command)
			output, err := process.CombinedOutput()
			if err == nil || process.ProcessState.ExitCode() != 7 || string(output) != tc.output {
				t.Fatalf("template changed execution: %q -> %s (%v)", command, output, err)
			}
			id, err := os.ReadFile(idFile)
			if err != nil || string(id) != "call-review" {
				t.Fatalf("worker lost identity: %q (%v)", id, err)
			}
			if got := inspectionAXCallID(mustMarshalJSON(command)); got != "call-review" {
				t.Fatalf("template lost offline join: %q", got)
			}
		})
	}
}

func TestAXCommandInspectionRequiresCarrierProvenance(t *testing.T) {
	registry := &toolRegistry{}
	contribution := toolContribution{PluginID: builtinToolsPluginID, Name: "shell"}
	for _, script := range []string{"curl example.invalid", "printf builtin"} {
		command, err := registry.execCarrierCommand(contribution, script, []string{"bash", script}, "", 0, "call-review")
		if err != nil {
			t.Fatal(err)
		}
		for _, encoded := range []json.RawMessage{mustMarshalJSON(command), mustMarshalJSON([]string{"/bin/bash", "-lc", command})} {
			if got := inspectionAXCallID(encoded); got != "call-review" {
				t.Fatalf("generated carrier lost identity: %q -> %q", command, got)
			}
		}
		if strings.HasPrefix(script, "curl") && strings.Contains(command, "shell bash") {
			t.Fatal("AX disabled direct execution")
		}
		ordinary, err := registry.execCarrierCommand(contribution, script, []string{"bash", script}, "", 0)
		if err != nil || inspectionAXCallID(mustMarshalJSON(ordinary)) != "" {
			t.Fatalf("uninstrumented carrier acquired identity: %q %v", ordinary, err)
		}
	}
	for _, command := range []string{
		`MEKUGI_AX_CALL_ID=call-review curl example.invalid`,
		`MEKUGI_AX_CALL_ID=call-review shell bash ':'`,
		"# mekugi:ax:call_id=/private/path\ncurl example.invalid",
		"# mekugi:ax:call_id=call-review",
		"# mekugi:ax:call_id=call-review\n",
		"# mekugi:ax:call_id=call-review\n'",
		"echo '# mekugi:ax:call_id=call-review'",
	} {
		if got := inspectionAXCallID(mustMarshalJSON(command)); got != "" {
			t.Errorf("unrecognized command acquired identity: %q -> %q", command, got)
		}
	}
}

func TestDebugAXDiscoveryRejectsInvalidMetadata(t *testing.T) {
	for _, header := range []string{"", "{", `{"type":"session_meta","payload":{}}`, `{"type":"turn_context","payload":{"id":"thread"}}`} {
		t.Run(header, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CODEX_HOME", root)
			sessions := filepath.Join(root, "sessions")
			if err := os.Mkdir(sessions, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sessions, "rollout-thread.jsonl"), []byte(header+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if found, err := discoverDebugRollouts(t.Context(), []string{"thread"}); err == nil || found != nil {
				t.Fatalf("invalid metadata certified discovery: %v, %v", found, err)
			}
		})
	}
}
