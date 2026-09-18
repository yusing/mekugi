package mekugi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestErrorHookReceivesFailureAndRepairContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("present words\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDirectory := t.TempDir()
	bodyPath := filepath.Join(t.TempDir(), "body.md")
	writeSettingsForTest(t, dataDirectory, []string{
		"printf '%s' {{shellquote (format_markdown .)}} > " + shellQuote(bodyPath),
	})

	edits := []FileEdit{{Path: "note.txt", Script: "type 1:" + hashLine("present words") + " \"missing\" \"replacement\"\n"}}
	result, applyErr := applyForHostAtTest(t, root, edits, dataDirectory)
	if applyErr == nil {
		t.Fatal("ApplyForHost() unexpectedly succeeded")
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"Command: 1 `type`",
		"Source: note.txt:1",
	} {
		if !strings.Contains(string(body), fragment) {
			t.Fatalf("hook body does not contain %q:\n%s", fragment, body)
		}
	}
	for _, omitted := range []string{"# mekugi command failed", "Description:", "Outcome:", "Category:", "Failed command", "Failure", "Diagnostic", "Repair context"} {
		if strings.Contains(string(body), omitted) {
			t.Fatalf("hook body unexpectedly contains %q:\n%s", omitted, body)
		}
	}
	if strings.Contains(result.Diagnostic, "warning:") {
		t.Fatalf("successful hook produced warning: %q", result.Diagnostic)
	}
}

func TestReportIssueRunsDiagnoseHooksWithExactMarkdown(t *testing.T) {
	dataDirectory := t.TempDir()
	bodyPath := filepath.Join(t.TempDir(), "body.md")
	diagnoseHooks := NewDiagnoseHooks(dataDirectory)
	content, err := json.Marshal(settings{Hooks: hooks{
		Error: []string{"exit 9"},
		Diagnose: []string{
			"printf '%s\n%s' {{shellquote .Title}} {{shellquote (format_markdown .)}} > " + shellQuote(bodyPath),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), content, 0o600); err != nil {
		t.Fatal(err)
	}

	markdown := "# Misleading repair context\n\nThe suggested target cannot match."
	if err := diagnoseHooks.Report(t.Context(), markdown); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "mekugi diagnostic\n" + markdown
	if string(body) != want {
		t.Fatalf("diagnose hook output = %q, want %q", body, want)
	}
}

func TestReportIssueReturnsDiagnoseHookFailure(t *testing.T) {
	dataDirectory := t.TempDir()
	diagnoseHooks := NewDiagnoseHooks(dataDirectory)
	content, err := json.Marshal(settings{Hooks: hooks{Diagnose: []string{"exit 9"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), content, 0o600); err != nil {
		t.Fatal(err)
	}

	err = diagnoseHooks.Report(t.Context(), "diagnostic")
	if err == nil || !strings.Contains(err.Error(), "running diagnose hook 1: exit status 9") {
		t.Fatalf("ReportIssue() error = %v", err)
	}
}

func TestErrorHookReceivesMalformedCommand(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "note.txt", "", 0o644)
	dataDirectory := t.TempDir()
	bodyPath := filepath.Join(t.TempDir(), "body.md")
	writeSettingsForTest(t, dataDirectory, []string{
		"printf '%s' {{shellquote .Body}} > " + shellQuote(bodyPath),
	})

	if _, err := applyForHostAtTest(t, root, []FileEdit{{Path: "note.txt", Script: "select the file\n"}}, dataDirectory); err == nil {
		t.Fatal("ApplyForHost() unexpectedly succeeded")
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Command: 1 `select`") {
		t.Fatalf("hook body does not contain command:\n%s", body)
	}
	if strings.Contains(string(body), "Description:") || strings.HasPrefix(string(body), "#") {
		t.Fatalf("error hook body unexpectedly contains title or description:\n%s", body)
	}
}

func TestErrorHookFailureDoesNotReplaceDiagnostic(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "note.txt", "", 0o644)
	dataDirectory := t.TempDir()
	writeSettingsForTest(t, dataDirectory, []string{"exit 7"})

	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "note.txt", Script: "unknown-command\n"}}, dataDirectory)
	if err == nil {
		t.Fatal("ApplyForHost() unexpectedly succeeded")
	}
	if !strings.HasPrefix(result.Diagnostic, "unknown-command: command 1, path \"note.txt\", reason script-syntax: unknown or malformed command\n") {
		t.Fatalf("original diagnostic was not preserved: %q", result.Diagnostic)
	}
	if !strings.Contains(result.Diagnostic, "mekugi: warning: running error hook 1: exit status 7\n") {
		t.Fatalf("hook failure was not reported: %q", result.Diagnostic)
	}
}

func TestSettingsAreReadOnlyForEvaluationFailures(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "note.txt", "", 0o644)
	dataDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := applyForHostAtTest(t, root, []FileEdit{{Path: "note.txt", Script: `append "ok"`}}, dataDirectory); err != nil {
		t.Fatalf("successful ApplyForHost() error = %v", err)
	}
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "note.txt", Script: "unknown-command\n"}}, dataDirectory)
	if err == nil || !strings.Contains(result.Diagnostic, "mekugi: warning: decoding settings:") {
		t.Fatalf("failed ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
}

func TestEnvironmentalCommandFailureDoesNotRunErrorHook(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	dataDirectory := t.TempDir()
	bodyPath := filepath.Join(t.TempDir(), "body.md")
	writeSettingsForTest(t, dataDirectory, []string{"touch " + shellQuote(bodyPath)})

	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "folder", Script: ""}}, dataDirectory)
	if err == nil || !strings.Contains(result.Diagnostic, "folder is not a regular file") {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
		t.Fatalf("environmental failure ran hook: stat error %v", err)
	}
}

func TestExecuteErrorHookTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := executeErrorHook(ctx, "sleep 10")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("executeErrorHook() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("executeErrorHook() took %s", elapsed)
	}
}

func TestAggregatedErrorHooksShareOneTimeout(t *testing.T) {
	dataDirectory := t.TempDir()
	writeSettingsForTest(t, dataDirectory, []string{"sleep 10"})
	sourceErrors := []*commandError{
		{Reason: reasonSyntax, Command: 1, Line: 1, Operation: "bad", Category: "syntax", Source: "bad", Message: "unknown command"},
		{Reason: reasonSyntax, Command: 2, Line: 2, Operation: "bad", Category: "syntax", Source: "bad", Message: "unknown command"},
	}

	started := time.Now()
	errs := runCommandErrorHooks(t.Context(), dataDirectory, sourceErrors, "failed", 20*time.Millisecond)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("runCommandErrorHooks() took %s", elapsed)
	}
	if len(errs) != 1 || !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Fatalf("runCommandErrorHooks() errors = %v", errs)
	}
}

func TestErrorHooksShareOneTimeout(t *testing.T) {
	dataDirectory := t.TempDir()
	writeSettingsForTest(t, dataDirectory, []string{"sleep 10", "sleep 10"})
	sourceError := &commandError{Reason: reasonSyntax, Command: 1, Line: 1, Operation: "bad", Category: "syntax", Source: "bad", Message: "unknown command"}

	started := time.Now()
	errs := runCommandErrorHooks(t.Context(), dataDirectory, []*commandError{sourceError}, failureDiagnostic(sourceError.Error()), 20*time.Millisecond)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("runCommandErrorHooks() took %s", elapsed)
	}
	if len(errs) != 1 || !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Fatalf("runCommandErrorHooks() errors = %v", errs)
	}
}

func TestReadSettingsRejectsOversizeContent(t *testing.T) {
	dataDirectory := t.TempDir()
	content := append([]byte(`{"hooks":{"error":[]}}`), bytes.Repeat([]byte(" "), maxSettingsBytes)...)
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), content, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readSettings(dataDirectory)
	if err == nil || !strings.Contains(err.Error(), "file exceeds 1048576 bytes") {
		t.Fatalf("readSettings() error = %v", err)
	}
}

func TestMarkdownCodeSpanHandlesBackticks(t *testing.T) {
	body := formatErrorHookMarkdown(errorHookEvent{Command: 1, Operation: "type`quoted"})
	if !strings.Contains(body, "Command: 1 `` type`quoted ``") {
		t.Fatalf("formatErrorHookMarkdown() = %q", body)
	}
}

func TestOutcomeHookMarkdownUsesSafeFence(t *testing.T) {
	event := outcomeHookEvent{
		attemptHookFields: attemptHookFields{Outcome: "succeeded"},
		Title:             "mekugi attempt succeeded",
		EmittedPayload:    "type <<PATCH\n```\nPATCH\n",
	}
	body := formatOutcomeHookMarkdown(event)
	if event.Title != "mekugi attempt succeeded" {
		t.Fatalf("outcome title = %q", event.Title)
	}
	if !strings.Contains(body, "````mekugi\ntype <<PATCH\n```\nPATCH\n````") {
		t.Fatalf("formatOutcomeHookMarkdown() = %q", body)
	}
	if strings.HasPrefix(body, "#") {
		t.Fatalf("outcome hook body unexpectedly contains title: %q", body)
	}
}

func TestOutcomeHookFailureWarnsWithoutReplacingSuccess(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "note.txt", "", 0o644)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dataDirectory := t.TempDir()
	content, err := json.Marshal(settings{Hooks: hooks{Outcome: []string{"exit 9"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), content, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithAttemptMetadata(t.Context(), AttemptMetadata{SessionID: "session", CorrelationID: "chain", CallID: "call", Attempt: 1})
	translated, err := translateForHostForTest(ctx, Workspace{Root: root}, []FileEdit{{Path: "note.txt", Script: `append "ok"`}}, dataDirectory)
	if err != nil || len(translated.Patch) == 0 {
		t.Fatalf("translation = %+v, error %v", translated, err)
	}
	if !strings.Contains(translated.Diagnostic, "warning: running outcome hook 1: exit status 9") {
		t.Fatalf("outcome warning = %q", translated.Diagnostic)
	}
}

func TestRejectedAttemptReportsSettingsFailureOnce(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dataDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithAttemptMetadata(t.Context(), AttemptMetadata{SessionID: "session", CorrelationID: "chain", CallID: "call", Attempt: 1})

	translated, err := translateForHostForTest(ctx, Workspace{Root: root}, []FileEdit{{Path: "note.txt", Script: "unknown-command\n"}}, dataDirectory)
	if err == nil {
		t.Fatalf("translateForHostForTest() translation = %+v, want rejection", translated)
	}
	if count := strings.Count(translated.Diagnostic, "mekugi: warning: decoding settings:"); count != 1 {
		t.Fatalf("settings warning count = %d, diagnostic:\n%s", count, translated.Diagnostic)
	}
}

func TestErrorAndOutcomeHooksReceiveAttemptMetadata(t *testing.T) {
	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "note.txt", "", 0o644)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dataDirectory := t.TempDir()
	errorPath := filepath.Join(t.TempDir(), "error.md")
	outcomePath := filepath.Join(t.TempDir(), "outcome.md")
	metadataPath := filepath.Join(t.TempDir(), "metadata.txt")
	titlePath := filepath.Join(filepath.Dir(metadataPath), "outcome-title.txt")
	content, err := json.Marshal(settings{Hooks: hooks{
		Error: []string{
			"printf '%s' {{shellquote (format_markdown .)}} > " + shellQuote(errorPath),
		},
		Outcome: []string{
			"printf '%s' {{shellquote (format_markdown .)}} > " + shellQuote(outcomePath),
			"printf '%s' {{shellquote .CorrelationID}}'|'{{shellquote .CallID}}'|'{{.Attempt}}'|'{{.Correction}}'|'{{shellquote .ToolName}}'|'{{shellquote .Stage}}'|'{{shellquote .Outcome}}'|'{{.EmittedBytes}}'|'{{.EvaluatedBytes}}'|'{{.PatchBytes}} > " + shellQuote(metadataPath),
			"printf '%s' {{shellquote .Title}} > " + shellQuote(titlePath),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), content, 0o600); err != nil {
		t.Fatal(err)
	}

	rejectedScript := "unknown-command\n"
	rejectedMetadata := AttemptMetadata{
		SessionID:       "session-1",
		CorrelationID:   "chain-1",
		CallID:          "call-1",
		Attempt:         1,
		Model:           "gpt-5.6-sol medium",
		ToolName:        "functions.hpatch",
		EmittedPayload:  rejectedScript,
		EvaluatedScript: rejectedScript,
	}
	failed, err := translateForHostForTest(
		WithAttemptMetadata(t.Context(), rejectedMetadata),
		Workspace{Root: root},
		[]FileEdit{{Path: "note.txt", Script: rejectedScript}},
		dataDirectory,
	)
	if err == nil || failed.Diagnostic == "" {
		t.Fatalf("failed translation = %+v, error %v", failed, err)
	}
	if _, err := os.Stat(errorPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("routed rejection invoked command-error hook: %v", err)
	}
	outcome, err := os.ReadFile(outcomePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Tool: `functions.hpatch`",
		"Stage: `evaluated`",
		"Outcome: `rejected`",
		"## Emitted HPATCH script",
		"```mekugi\nunknown-command\n```",
	} {
		if !strings.Contains(string(outcome), want) {
			t.Fatalf("rejected outcome hook lacks %q:\n%s", want, outcome)
		}
	}

	evaluatedScript := `append "ok"`
	recoveryPayload := "C2:abcd 2:bbbb"
	delta := "C2:abcd: 1:aaaa -> 2:bbbb"
	recoveryMetadata := AttemptMetadata{
		SessionID:       "session-1",
		CorrelationID:   "chain-1",
		CallID:          "call-2",
		Attempt:         2,
		Correction:      true,
		Model:           "gpt-5.6-sol medium",
		ToolName:        "functions.hpatch_recover",
		EmittedPayload:  recoveryPayload,
		EvaluatedScript: evaluatedScript,
		RecoveryDelta:   delta,
		Title:           "Update note",
	}
	translated, err := translateForHostForTest(
		WithAttemptMetadata(t.Context(), recoveryMetadata),
		Workspace{Root: root},
		[]FileEdit{{Path: "note.txt", Script: evaluatedScript}},
		dataDirectory,
	)
	if err != nil || translated.Diagnostic != "" {
		t.Fatalf("successful translation = %+v, error %v", translated, err)
	}
	outcomeTitle, err := os.ReadFile(titlePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(outcomeTitle) != "mekugi recovery attempt succeeded: Update note" {
		t.Fatalf("outcome hook title = %q", outcomeTitle)
	}
	outcome, err = os.ReadFile(outcomePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Tool: `functions.hpatch_recover`",
		"Stage: `translated`",
		"Outcome: `succeeded`",
		"## Emitted recovery payload",
		"```mekugi-recover\n" + recoveryPayload + "\n```",
		"## Resolved recovery delta",
		"    " + delta,
		fmt.Sprintf("Router rebuilt a %d-byte complete HPATCH script; it was not model-emitted.", len(evaluatedScript)),
	} {
		if !strings.Contains(string(outcome), want) {
			t.Fatalf("recovery outcome hook lacks %q:\n%s", want, outcome)
		}
	}
	if strings.Contains(string(outcome), "```mekugi\n"+evaluatedScript) {
		t.Fatalf("recovery outcome presents rebuilt script as emitted:\n%s", outcome)
	}
	metadataBody, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	wantMetadata := fmt.Sprintf(
		"chain-1|call-2|2|true|functions.hpatch_recover|translated|succeeded|%d|%d|%d",
		len(recoveryPayload),
		len(evaluatedScript),
		len(translated.Patch),
	)
	if string(metadataBody) != wantMetadata {
		t.Fatalf("outcome metadata = %q, want %q", metadataBody, wantMetadata)
	}
}

func TestApplicationFailureReportsAppliedStage(t *testing.T) {
	dataDirectory := t.TempDir()
	metadataPath := filepath.Join(t.TempDir(), "metadata.txt")
	content, err := json.Marshal(settings{Hooks: hooks{Outcome: []string{
		"printf '%s' {{shellquote .Stage}}'|'{{shellquote .Outcome}} > " + shellQuote(metadataPath),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), content, 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := AttemptMetadata{
		SessionID:       "session",
		CorrelationID:   "chain",
		CallID:          "call",
		Attempt:         1,
		ToolName:        "functions.hpatch",
		EmittedPayload:  "new note.txt\ntype \"ok\"\n",
		EvaluatedScript: "new note.txt\ntype \"ok\"\n",
	}
	_, err = finishHostChange(
		WithAttemptMetadata(t.Context(), metadata),
		dataDirectory,
		metadata.EvaluatedScript,
		HostTranslation{},
		"applied",
		errors.New("changing note.txt: permission denied"),
		true,
	)
	if err == nil {
		t.Fatal("application failure succeeded")
	}
	got, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "applied|failed" {
		t.Fatalf("outcome metadata = %q", got)
	}
}

func writeSettingsForTest(t *testing.T, dataDirectory string, errorHooks []string) {
	t.Helper()
	content, err := json.Marshal(settings{Hooks: hooks{Error: errorHooks}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, settingsFilename), content, 0o600); err != nil {
		t.Fatal(err)
	}
}
