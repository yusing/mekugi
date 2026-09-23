package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

const (
	reportIssueToolName        = "report_issue"
	reportIssueHistoryTool     = "__mekugi_report_issue"
	reportIssueToolDescription = "Report an observed Mekugi interaction problem as Markdown to the configured diagnose hooks."
	maxDiagnoseSettingsBytes   = 1 << 20
	diagnoseTimeout            = 10 * time.Second
)

func exposeReportIssueTool(fields map[string]json.RawMessage, catalog *responsesToolCatalog) error {
	var check func(*responsesToolSection) error
	check = func(section *responsesToolSection) error {
		if section == nil || section.err != nil {
			return errors.New("decode tool catalog for issue reporting")
		}
		for _, tool := range section.tools {
			if tool == nil {
				continue
			}
			if tool.Name == reportIssueToolName || tool.Name == "functions."+reportIssueToolName {
				return errors.New("request already defines report_issue")
			}
			if tool.nested != nil {
				if err := check(tool.nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(catalog.top); err != nil {
		return err
	}
	for _, group := range catalog.additional {
		if err := check(group.tools); err != nil {
			return err
		}
	}
	catalog.appendTop([]*responsesToolDefinition{newResponsesToolDefinition(map[string]json.RawMessage{
		"type":        mustMarshalJSON("function"),
		"name":        mustMarshalJSON(reportIssueToolName),
		"description": mustMarshalJSON(reportIssueToolDescription),
		"strict":      mustMarshalJSON(false),
		"parameters": mustMarshalJSON(map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"markdown": map[string]any{"type": "string", "description": "The exact Markdown issue report."},
			},
			"required": []string{"markdown"},
		}),
	})})
	return catalog.encodeTop(fields)
}

func isReportIssueCall(item map[string]json.RawMessage) bool {
	return jsonString(item, "type") == "function_call" && jsonString(item, "name") == reportIssueToolName &&
		(jsonString(item, "namespace") == "" || jsonString(item, "namespace") == "functions")
}

func (t *mekugiResponseTransform) executeReportIssueCall(item map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if !t.proxy.registry.diagnoseEnabled {
		return nil, errors.New("report_issue is unavailable")
	}
	callID := jsonString(item, "call_id")
	if callID == "" {
		return nil, errors.New("report_issue call requires a call ID")
	}
	if prior := t.journalCalls[callID]; prior != nil {
		if !isReportIssueCall(prior) || jsonString(prior, "arguments") != jsonString(item, "arguments") {
			return nil, errors.New("report_issue call changed arguments")
		}
		for _, result := range t.journalResults {
			if jsonString(result, "call_id") == callID {
				return result, nil
			}
		}
	}
	var args struct {
		Markdown string `json:"markdown"`
	}
	decoder := json.NewDecoder(strings.NewReader(jsonString(item, "arguments")))
	decoder.DisallowUnknownFields()
	var output string
	if err := decoder.Decode(&args); err != nil || decoder.Decode(new(any)) != io.EOF || len(args.Markdown) > maxMekugiScriptBytes {
		output = "mekugi: warning: invalid report_issue arguments"
	} else if err := t.proxy.registry.diagnoseHooks.report(t.ctx, args.Markdown, t.proxy.titles.title(t.sessionID)); err != nil {
		output = "Issue report was not delivered.\nmekugi: warning: " + err.Error()
	} else {
		output = "Issue reported."
	}
	result := map[string]json.RawMessage{
		"type":    mustMarshalJSON("function_call_output"),
		"call_id": mustMarshalJSON(callID),
		"output":  mustMarshalJSON(output),
	}
	t.recordLocal(callID, &mekugiHistory{
		ToolName: reportIssueHistoryTool, Script: jsonString(item, "arguments"),
		CarrierKind: codeModeCarrierFunction, CarrierName: reportIssueToolName,
		CarrierPayload: jsonString(item, "arguments"), UpstreamItem: maps.Clone(item),
	})
	if err := t.commitLocalCall(callID); err != nil {
		return nil, err
	}
	t.journalCalls[callID] = maps.Clone(item)
	t.journalResults = append(t.journalResults, result)
	return result, nil
}

// diagnoseHooks is the startup snapshot of the user's optional issue-report
// commands. It is not an editing hook and never runs for apply_patch.
type diagnoseHooks []string

func loadDiagnoseHooks(dataDirectory string) (diagnoseHooks, error) {
	file, err := os.Open(filepath.Join(dataDirectory, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read diagnose settings: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxDiagnoseSettingsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read diagnose settings: %w", err)
	}
	if len(content) > maxDiagnoseSettingsBytes {
		return nil, fmt.Errorf("diagnose settings exceed %d bytes", maxDiagnoseSettingsBytes)
	}
	var settings struct {
		Hooks struct {
			Error    []string `json:"error"`
			Diagnose []string `json:"diagnose"`
			Outcome  []string `json:"outcome"`
		} `json:"hooks"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return nil, fmt.Errorf("decode diagnose settings: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode diagnose settings: trailing JSON value")
	}
	return append(diagnoseHooks(nil), settings.Hooks.Diagnose...), nil
}

func (hooks diagnoseHooks) report(ctx context.Context, markdown, title string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(hooks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, diagnoseTimeout)
	defer cancel()
	if title == "" {
		title = "mekugi diagnostic"
	}
	event := struct{ Body, Title string }{Body: markdown, Title: title}
	var hookErrors []error
	for index, source := range hooks {
		tmpl, err := template.New("diagnose").Option("missingkey=error").Funcs(template.FuncMap{
			"format_markdown": func(any) string { return markdown },
			"shellquote": func(value string) string {
				return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
			},
		}).Parse(source)
		if err == nil {
			var command bytes.Buffer
			err = tmpl.Execute(&command, event)
			if err == nil && strings.TrimSpace(command.String()) == "" {
				err = errors.New("rendered command is empty")
			}
			if err == nil {
				err = exec.CommandContext(ctx, "/bin/sh", "-c", command.String()).Run()
				if ctx.Err() != nil {
					err = ctx.Err()
				}
			}
		}
		if err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("diagnose hook %d: %w", index+1, err))
			if ctx.Err() != nil {
				break
			}
		}
	}
	return errors.Join(hookErrors...)
}
