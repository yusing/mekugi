package router

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHpatchCheckpointRevisionAndExpiry(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	state, err := transform.retainMixedScript("", "", "shell true", []hpatchResumeSegment{
		{Source: "true", Program: "", Line: 1, Kind: "shell"},
	})
	if err != nil {
		t.Fatal(err)
	}
	name, _ := mixedArtifactName(state.Handle)
	path := filepath.Join(transform.shellDirectory, name)
	root, err := os.OpenRoot(transform.shellDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	progress := `{"index":0,"results":[],"operations":[]}`
	var output bytes.Buffer
	if err := runHpatchCheckpoint(t.Context(), root, state.Handle, 0, progress, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "1\n" {
		t.Fatalf("revision = %q", &output)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runHpatchCheckpoint(t.Context(), root, state.Handle, 0, progress, &output); err == nil || output.Len() != 0 {
		t.Fatalf("stale checkpoint accepted: %v, %q", err, &output)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected checkpoint changed retained state")
	}
	if err := json.Unmarshal(before, &state); err != nil {
		t.Fatal(err)
	}
	state.ExpiresAt = time.Now().Add(-time.Second)
	if err := os.WriteFile(path, mustMarshalJSON(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runHpatchCheckpoint(t.Context(), root, state.Handle, state.Revision, progress, &output); err == nil {
		t.Fatal("expired checkpoint accepted")
	}
	history, err := transform.translate("expired-resume", "resume "+state.Handle, nil)
	if err != nil || history.TranslationError == "" || history.CarrierPayload != "text("+strconv.Quote(history.TranslationError)+");" {
		t.Fatalf("expired handle emitted a carrier: %+v, %v", history, err)
	}

	// Keep the directory alive, so only per-artifact expiry can remove the lock.
	if _, _, ok := transform.proxy.retainShell(transform.shellDirectory, "keep-alive", "true"); !ok {
		t.Fatal("retain other artifact")
	}
	transform.proxy.mu.Lock()
	session := transform.proxy.shellSessions[transform.shellDirectory]
	session.timers[name].Stop()
	session.timers[name] = nil
	session.retireIdle()
	transform.proxy.mu.Unlock()
	for _, removed := range []string{path, path + ".lock"} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expired artifact survived: %s: %v", removed, err)
		}
	}
	if _, err := os.Stat(filepath.Join(transform.shellDirectory, "keep-alive")); err != nil {
		t.Fatal("expiry removed another artifact")
	}
}

func TestHpatchResumeThreadAndWorkspaceIsolation(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	state, err := transform.retainMixedScript("", "", "shell true", []hpatchResumeSegment{
		{Source: "true", Program: "", Line: 1, Kind: "shell"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := mixedTestTransform(t)
	history, err := other.translate("other-thread", "resume "+state.Handle, nil)
	if err != nil || history.TranslationError == "" || history.CarrierPayload != "text("+strconv.Quote(history.TranslationError)+");" {
		t.Fatalf("other thread resumed work: %+v, %v", history, err)
	}
	transform.directory = t.TempDir()
	history, err = transform.translate("other-workspace", "resume "+state.Handle, nil)
	if err != nil || history.TranslationError == "" || history.CarrierPayload != "text("+strconv.Quote(history.TranslationError)+");" {
		t.Fatalf("other workspace resumed work: %+v, %v", history, err)
	}
}

func TestHpatchResumePreservesReplacementSource(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	state, err := transform.retainMixedScript("", "", "shell true", []hpatchResumeSegment{
		{Source: "true", Line: 1, Kind: "shell"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"printf done > file\\ ", "printf done  \n", "printf done\t"} {
		history, err := transform.translateMixedResume("replacement-"+source, "\nresume "+state.Handle+" retry\nshell "+source, nil)
		if err != nil || history.TranslationError != "" {
			t.Fatalf("replacement rejected: %v, %s", err, history.TranslationError)
		}
		var config struct {
			Replacement hpatchResumeSegment `json:"replacement"`
		}
		encoded, _, _ := strings.Cut(strings.TrimPrefix(history.CarrierPayload, "const mixedConfig = "), ";\n")
		if err := json.Unmarshal([]byte(encoded), &config); err != nil {
			t.Fatal(err)
		}
		if config.Replacement.Source != strings.TrimSuffix(source, "\n") {
			t.Fatalf("source = %q, want %q", config.Replacement.Source, source)
		}
	}
}
