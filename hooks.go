package mekugi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const (
	settingsFilename  = "settings.json"
	maxSettingsBytes  = 1 << 20
	errorHooksTimeout = 10 * time.Second
)

type settings struct {
	Hooks hooks `json:"hooks"`
}

type hooks struct {
	Error    []string `json:"error"`
	Diagnose []string `json:"diagnose"`
	Outcome  []string `json:"outcome"`
}

type attemptHookFields struct {
	SessionID      string
	CorrelationID  string
	CallID         string
	Attempt        int
	Correction     bool
	Model          string
	ToolName       string
	Stage          string
	Outcome        string
	EmittedBytes   int
	EvaluatedBytes int
	PatchBytes     int
}

type errorHookEvent struct {
	attemptHookFields

	Body          string
	Title         string
	Diagnostic    string
	Repair        string
	Command       int
	SourceLine    int
	Operation     string
	Category      string
	Path          string
	FailedCommand string
	Failure       string
}

type outcomeHookEvent struct {
	attemptHookFields

	Body            string
	Title           string
	EmittedPayload  string
	EvaluatedScript string
	RecoveryDelta   string
	Patch           string
}

type diagnoseHookEvent struct {
	Body  string
	Title string
}

func runCommandErrorHooks(ctx context.Context, dataDirectory string, sourceErrors []*commandError, diagnostic string, timeout time.Duration) []error {
	if dataDirectory == "" {
		return nil
	}
	hasHookableError := false
	for _, sourceError := range sourceErrors {
		if sourceError.Reason != reasonOther {
			hasHookableError = true
			break
		}
	}
	if !hasHookableError {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return []error{err}
	}

	configured, err := readSettings(dataDirectory)
	if err != nil {
		return []error{err}
	}
	if len(configured.Hooks.Error) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var hookErrors []error
	for _, sourceError := range sourceErrors {
		if sourceError.Reason == reasonOther {
			continue
		}
		event := newErrorHookEvent(ctx, sourceError, diagnostic)
		hookErrors = append(hookErrors, runRenderedHooks(ctx, "error", configured.Hooks.Error, event, event.Body)...)
		if ctx.Err() != nil {
			return hookErrors
		}
	}
	return hookErrors
}

// DiagnoseHooks reads and runs agent-report commands from a settings directory.
type DiagnoseHooks string

// NewDiagnoseHooks returns diagnose hooks backed by dataDirectory.
func NewDiagnoseHooks(dataDirectory string) DiagnoseHooks {
	return DiagnoseHooks(dataDirectory)
}

// Report reads the current settings and sends agent-authored Markdown to its diagnose hooks.
func (hooks DiagnoseHooks) Report(ctx context.Context, markdown string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if hooks == "" {
		return nil
	}
	configured, err := readSettings(string(hooks))
	if err != nil {
		return err
	}
	if len(configured.Hooks.Diagnose) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, errorHooksTimeout)
	defer cancel()
	event := diagnoseHookEvent{
		Body:  markdown,
		Title: "mekugi diagnostic",
	}
	if metadata, ok := attemptMetadataFromContext(ctx); ok && metadata.Title != "" {
		event.Title = metadata.Title
	}
	return errors.Join(runRenderedHooks(ctx, "diagnose", configured.Hooks.Diagnose, event, event.Body)...)
}

func runRenderedHooks(ctx context.Context, hookType string, configured []string, event any, body string) []error {
	var hookErrors []error
	for index, source := range configured {
		command, err := renderHook(source, event, body)
		if err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("rendering %s hook %d: %w", hookType, index+1, err))
			continue
		}
		if err := executeErrorHook(ctx, command); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("running %s hook %d: %w", hookType, index+1, err))
			if ctx.Err() != nil {
				return hookErrors
			}
		}
	}
	return hookErrors
}

func readSettings(dataDirectory string) (settings, error) {
	file, err := os.Open(filepath.Join(dataDirectory, settingsFilename))
	if errors.Is(err, os.ErrNotExist) {
		return settings{}, nil
	}
	if err != nil {
		return settings{}, fmt.Errorf("reading settings: %w", err)
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return settings{}, fmt.Errorf("reading settings: %w", err)
	}
	if len(content) > maxSettingsBytes {
		return settings{}, fmt.Errorf("reading settings: file exceeds %d bytes", maxSettingsBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var configured settings
	if err := decoder.Decode(&configured); err != nil {
		return settings{}, fmt.Errorf("decoding settings: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return settings{}, errors.New("decoding settings: trailing JSON value")
		}
		return settings{}, fmt.Errorf("decoding settings: %w", err)
	}
	return configured, nil
}

func newErrorHookEvent(ctx context.Context, sourceError *commandError, diagnostic string) errorHookEvent {
	event := errorHookEvent{
		Diagnostic:    strings.TrimSuffix(diagnostic, "\n"),
		Repair:        strings.TrimSuffix(sourceError.Repair, "\n"),
		Command:       sourceError.Command,
		SourceLine:    sourceError.Line,
		Operation:     sourceError.Operation,
		Category:      sourceError.Category,
		Path:          sourceError.Path,
		FailedCommand: sourceError.Source,
		Failure:       sourceError.Message,
	}
	event.Title = "mekugi command failed"
	if metadata, ok := attemptMetadataFromContext(ctx); ok {
		event.attemptHookFields = newAttemptHookFields(metadata, "evaluated", "rejected", 0)
		if metadata.Title != "" {
			event.Title = metadata.Title
		}
	}
	event.Body = formatErrorHookMarkdown(event)
	return event
}

func formatErrorHookMarkdown(event errorHookEvent) string {
	var body strings.Builder
	if event.Command != 0 {
		fmt.Fprintf(&body, "Command: %d", event.Command)
		if event.Operation != "" {
			fmt.Fprintf(&body, " %s", markdownCodeSpan(event.Operation))
		}
		body.WriteString("\n\n")
	}
	if event.Path != "" {
		fmt.Fprintf(&body, "Source: %s", event.Path)
		if event.SourceLine != 0 {
			fmt.Fprintf(&body, ":%d", event.SourceLine)
		}
		body.WriteString("\n\n")
	}
	if event.Model != "" {
		fmt.Fprintf(&body, "Model: %s\n\n", event.Model)
	}
	if event.SessionID != "" {
		fmt.Fprintf(&body, "Session ID: %s\n\n", event.SessionID)
	}
	if event.CallID != "" {
		fmt.Fprintf(&body, "Call ID: %s\n\n\n", event.CallID)
	}
	if event.Attempt != 0 {
		fmt.Fprintf(&body, "Attempt: %d\n\n", event.Attempt)
	}
	if event.SessionID != "" {
		fmt.Fprintf(&body, "Correction: %t\n\n", event.Correction)
	}
	return strings.TrimSuffix(body.String(), "\n")
}

func writeHookField(body *strings.Builder, label, value string) {
	if value == "" || value == "0" {
		return
	}
	fmt.Fprintf(body, "- %s: %s\n", label, markdownCodeSpan(value))
}

func markdownCodeSpan(value string) string {
	longestRun := 0
	for remaining := value; ; {
		start := strings.IndexByte(remaining, '`')
		if start < 0 {
			break
		}
		remaining = remaining[start:]
		run := len(remaining) - len(strings.TrimLeft(remaining, "`"))
		longestRun = max(longestRun, run)
		remaining = remaining[run:]
	}
	delimiter := strings.Repeat("`", longestRun+1)
	padding := ""
	if strings.Contains(value, "`") || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		padding = " "
	}
	return delimiter + padding + value + padding + delimiter
}

func writeHookBlock(body *strings.Builder, label, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(body, "\n## %s\n\n", label)
	for line := range strings.Lines(value) {
		body.WriteString("    ")
		body.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			body.WriteByte('\n')
		}
	}
}

func renderHook(source string, event any, body string) (string, error) {
	tmpl, err := template.New("hook").Option("missingkey=error").Funcs(template.FuncMap{
		"format_markdown": func(any) string { return body },
		"shellquote":      shellQuote,
	}).Parse(source)
	if err != nil {
		return "", err
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, event); err != nil {
		return "", err
	}
	if strings.TrimSpace(rendered.String()) == "" {
		return "", errors.New("rendered command is empty")
	}
	return rendered.String(), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func newAttemptHookFields(metadata AttemptMetadata, stage, outcome string, patchBytes int) attemptHookFields {
	return attemptHookFields{
		SessionID:      metadata.SessionID,
		CorrelationID:  metadata.CorrelationID,
		CallID:         metadata.CallID,
		Attempt:        metadata.Attempt,
		Correction:     metadata.Correction,
		Model:          metadata.Model,
		ToolName:       metadata.ToolName,
		Stage:          stage,
		Outcome:        outcome,
		EmittedBytes:   len(metadata.EmittedPayload),
		EvaluatedBytes: len(metadata.EvaluatedScript),
		PatchBytes:     patchBytes,
	}
}

func writeAttemptHookFields(body *strings.Builder, fields attemptHookFields) {
	if fields.SessionID == "" {
		return
	}
	writeHookField(body, "Session ID", fields.SessionID)
	writeHookField(body, "Correlation ID", fields.CorrelationID)
	writeHookField(body, "Call ID", fields.CallID)
	writeHookField(body, "Attempt", strconv.Itoa(fields.Attempt))
	writeHookField(body, "Tool", fields.ToolName)
	writeHookField(body, "Correction", strconv.FormatBool(fields.Correction))
	writeHookField(body, "Stage", fields.Stage)
	writeHookField(body, "Outcome", fields.Outcome)
	writeHookField(body, "Emitted bytes", strconv.Itoa(fields.EmittedBytes))
	writeHookField(body, "Evaluated bytes", strconv.Itoa(fields.EvaluatedBytes))
	writeHookField(body, "Patch bytes", strconv.Itoa(fields.PatchBytes))
}

func runOutcomeHooks(
	ctx context.Context,
	dataDirectory, stage, outcome, evaluatedScript string,
	patch []byte,
	timeout time.Duration,
) []error {
	metadata, ok := attemptMetadataFromContext(ctx)
	if !ok || dataDirectory == "" {
		return nil
	}
	configured, err := readSettings(dataDirectory)
	if err != nil {
		return []error{err}
	}
	if len(configured.Hooks.Outcome) == 0 {
		return nil
	}
	if metadata.ToolName == "" {
		metadata.ToolName = "functions.hpatch"
	}
	if metadata.EmittedPayload == "" {
		metadata.EmittedPayload = evaluatedScript
	}
	if metadata.EvaluatedScript == "" {
		metadata.EvaluatedScript = evaluatedScript
	}
	event := outcomeHookEvent{
		attemptHookFields: newAttemptHookFields(metadata, stage, outcome, len(patch)),
		Title:             "mekugi attempt " + outcome,
		EmittedPayload:    metadata.EmittedPayload,
		EvaluatedScript:   metadata.EvaluatedScript,
		RecoveryDelta:     metadata.RecoveryDelta,
		Patch:             string(patch),
	}
	if metadata.Correction {
		event.Title = "mekugi recovery attempt " + outcome
	}
	if metadata.Title != "" {
		event.Title += ": " + metadata.Title
	}
	event.Body = formatOutcomeHookMarkdown(event)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runRenderedHooks(ctx, "outcome", configured.Hooks.Outcome, event, event.Body)
}

func formatOutcomeHookMarkdown(event outcomeHookEvent) string {
	var body strings.Builder
	writeAttemptHookFields(&body, event.attemptHookFields)
	if event.EmittedPayload != "" {
		label := "Emitted HPATCH script"
		language := "mekugi"
		if event.Correction {
			label = "Emitted recovery payload"
			language = "mekugi-recover"
		}
		writeHookFencedBlock(&body, label, language, event.EmittedPayload)
	}
	writeHookBlock(&body, "Resolved recovery delta", event.RecoveryDelta)
	if event.Correction && event.EvaluatedScript != "" && event.EvaluatedScript != event.EmittedPayload {
		fmt.Fprintf(&body, "\nRouter rebuilt a %d-byte complete HPATCH script; it was not model-emitted.\n", len(event.EvaluatedScript))
	}
	return strings.TrimSuffix(body.String(), "\n")
}

func writeHookFencedBlock(body *strings.Builder, label, language, value string) {
	if value == "" {
		return
	}
	fence := markdownFence(value)
	fmt.Fprintf(body, "\n## %s\n\n%s%s\n%s", label, fence, language, value)
	if !strings.HasSuffix(value, "\n") {
		body.WriteByte('\n')
	}
	body.WriteString(fence)
	body.WriteByte('\n')
}

func markdownFence(value string) string {
	longestRun := 0
	for line := range strings.Lines(value) {
		run := len(line) - len(strings.TrimLeft(line, "`"))
		longestRun = max(longestRun, run)
	}
	return strings.Repeat("`", max(3, longestRun+1))
}

func executeErrorHook(ctx context.Context, command string) error {
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	// Nil output streams go directly to os.DevNull. Using io.Discard creates
	// copying pipes that descendants can hold open after the shell is canceled.
	err := process.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
