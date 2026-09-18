package router

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

func (t *mekugiResponseTransform) translateTool(name, callID, input string, upstreamItem map[string]json.RawMessage) (mekugiHistory, error) {
	for itemID := range t.previews {
		if t.pending[itemID].callID == callID {
			t.endPreview(itemID)
		}
	}
	switch name {
	case reportIssueToolName:
		return t.translateReportIssue(callID, input, upstreamItem)
	}
	contribution, ok := t.proxy.registry.contribution(name)
	if !ok || contribution.Builtin {
		return mekugiHistory{}, fmt.Errorf("registered tool %q is unavailable", name)
	}
	return t.translateRegisteredTool(contribution, callID, input, upstreamItem)
}

func (t *mekugiResponseTransform) translateReportIssue(callID, input string, upstreamItem map[string]json.RawMessage) (mekugiHistory, error) {
	if history, ok := t.local[callID]; ok {
		if history.ToolName != reportIssueToolName || history.Script != input {
			return mekugiHistory{}, fmt.Errorf("report_issue call %q changed input", callID)
		}
		if len(upstreamItem) != 0 {
			history.UpstreamItem = maps.Clone(upstreamItem)
			t.local[callID] = history
		}
		return history, nil
	}
	attemptContext := mekugi.WithAttemptMetadata(t.ctx, mekugi.AttemptMetadata{
		SessionID:       t.sessionID,
		Title:           t.proxy.titles.title(t.sessionID),
		CorrelationID:   callID,
		CallID:          callID,
		Attempt:         1,
		Model:           t.model,
		ToolName:        reportIssueToolName,
		EmittedPayload:  input,
		EvaluatedScript: input,
	})
	report := "Issue reported."
	if err := t.proxy.registry.DiagnoseHooks.Report(attemptContext, input); err != nil {
		report = "Issue report was not delivered.\nmekugi: warning: " + strings.TrimSpace(err.Error()) + "\n"
	}
	history := mekugiHistory{
		ToolName:     reportIssueToolName,
		Script:       input,
		CarrierName:  t.codeModeToolName,
		Report:       report,
		Applied:      true,
		UpstreamItem: maps.Clone(upstreamItem),
	}
	t.recordLocal(callID, &history)
	return history, nil
}

func (t *mekugiResponseTransform) translateRegisteredTool(contribution toolContribution, callID, input string, upstreamItem map[string]json.RawMessage) (mekugiHistory, error) {
	if history, ok := t.local[callID]; ok {
		if history.ToolName != contribution.Name || history.PluginID != contribution.PluginID || history.Script != input {
			return mekugiHistory{}, fmt.Errorf("%s call %q changed input", contribution.Name, callID)
		}
		if len(upstreamItem) != 0 {
			history.UpstreamItem = maps.Clone(upstreamItem)
			t.local[callID] = history
		}
		return history, nil
	}
	axCallID := ""
	if t.featureTrace.debug != nil || os.Getenv(capturer.AXReadOutputEnvironment) != "" {
		axCallID = callID
	}
	journalToken := t.subscribeShellJournal(callID, contribution)
	if contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell" {
		t.proxy.autoLiveDiff.requestLaunch(t.directory, t.threadID)
	}
	recovered := !t.nativeTools && shellCodeModeRecovery(contribution, input)
	var batch []string
	var translation toolplugin.Translation
	var err error
	if !recovered && contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell" {
		var programs []string
		programs, err = shellsyntax.Split(input)
		if err == nil && len(programs) > 1 {
			batch, translation, err = t.prepareShellBatch(contribution, programs, axCallID, journalToken)
		}
		if err != nil {
			translation = toolplugin.Translation{Rejected: true, Diagnostic: err.Error()}
		}
	}
	if !recovered && !translation.Rejected && len(batch) == 0 {
		if contribution.PluginID == builtinToolsPluginID {
			translation, err = t.proxy.registry.builtinTranslator.Translate(t.ctx, contribution.ModuleIndex, input)
		} else {
			translation, err = toolplugin.Translate(
				t.ctx,
				t.proxy.registry.NodeExecutable,
				t.proxy.registry.RuntimeRoot,
				contribution.Module,
				contribution.ModuleIndex,
				input,
			)
		}
		if err != nil {
			return mekugiHistory{}, fmt.Errorf("translate registered tool %s: %w", contribution.Name, err)
		}
	}
	if !recovered && !translation.Rejected && len(batch) == 0 && shellTypeScriptMisuse(contribution, translation.Arguments) {
		translation = toolplugin.Translation{Rejected: true, Diagnostic: shellTypeScriptDiagnostic}
	}

	kind := codeModeCarrierCustom
	if t.nativeTools {
		kind = codeModeCarrierFunction
	}
	name := t.codeModeToolName
	payload := ""
	diagnostic := translation.Diagnostic
	splitShellCarrier := false
	var misuseWarnings []string
	if recovered {
		misuseWarnings = append(misuseWarnings, shellCodeModeRecoveryWarning)
		if inspectCodeModeRuntime(input).execCommand {
			misuseWarnings = append(misuseWarnings, nativeExecCommandWarning)
		}
		payload = input
		if err := t.carriers.require(name, kind); err != nil {
			return mekugiHistory{}, err
		}
	} else if translation.Rejected {
		if err := t.carriers.require(name, kind); err != nil {
			return mekugiHistory{}, fmt.Errorf("%s input rejection: %w", contribution.Name, err)
		}
		if diagnostic == "" {
			diagnostic = contribution.Name + " rejected the model input"
		}
		if t.nativeTools {
			command := "printf %s " + shellQuoteArgument(diagnostic)
			if diagnostic == shellTypeScriptDiagnostic {
				command = mekugiNativeDiagnosticMarker + strconv.Quote(diagnostic) + "\n" + command
			}
			payload = renderExecCarrier(
				kind,
				execCommandArguments(command, nil),
				false,
			)
		} else {
			payload = "text(" + strconv.Quote(diagnostic) + ");"
		}
	} else {
		switch translation.Carrier.Kind {
		case "exec":
			if err := t.carriers.require(name, kind); err != nil {
				return mekugiHistory{}, fmt.Errorf("%s exec carrier: %w", contribution.Name, err)
			}
			if len(batch) != 0 {
				payload = renderShellBatch(batch)
				splitShellCarrier = true
				break
			}
			arguments := translation.Arguments
			if splitPayload, ok := t.shellCatCarrier(contribution, kind, arguments, translation.Carrier.Template, translation.Carrier.Params, axCallID, journalToken); ok {
				payload = splitPayload
				splitShellCarrier = true
				break
			}
			payload, err = t.proxy.registry.execCarrierPayload(
				kind,
				contribution,
				input,
				arguments,
				translation.Carrier.Template,
				translation.Carrier.Params,
				axCallID, journalToken,
			)
			if err != nil {
				return mekugiHistory{}, fmt.Errorf("%s exec carrier: %w", contribution.Name, err)
			}
		case "custom":
			kind = codeModeCarrierCustom
			name = translation.Carrier.Name
			payload = translation.Carrier.Payload
			if err := t.carriers.require(name, kind); err != nil {
				return mekugiHistory{}, fmt.Errorf("%s custom carrier: %w", contribution.Name, err)
			}
		case "function":
			kind = codeModeCarrierFunction
			name = translation.Carrier.Name
			payload = translation.Carrier.Payload
			if err := t.carriers.require(name, kind); err != nil {
				return mekugiHistory{}, fmt.Errorf("%s function carrier: %w", contribution.Name, err)
			}
			var arguments map[string]json.RawMessage
			if json.Unmarshal([]byte(payload), &arguments) != nil || arguments == nil {
				return mekugiHistory{}, fmt.Errorf("%s function carrier returned invalid JSON object arguments", contribution.Name)
			}
		default:
			return mekugiHistory{}, fmt.Errorf(
				"%s translator returned unsupported carrier kind %q",
				contribution.Name,
				translation.Carrier.Kind,
			)
		}
	}
	if !translation.Rejected && !splitShellCarrier {
		for _, misuse := range shellInterpreterWrapperMisuses(contribution, input) {
			misuseWarnings = append(misuseWarnings, shellInterpreterWrapperWarning(misuse))
		}
	}
	misuseWarning := ""
	outputWarning := ""
	if recovered {
		usage := inspectCodeModeRuntime(payload)
		if usage.textShadowed {
			outputWarning = strings.Join(misuseWarnings, "\n") + "\n"
		} else {
			for _, warning := range misuseWarnings {
				misuseWarning += misuseWarningProjection(warning)
			}
		}
		offset := usage.warningOffset
		payload = payload[:offset] + misuseWarning + payload[offset:]
	} else if t.nativeTools && len(misuseWarnings) != 0 {
		var arguments map[string]json.RawMessage
		if json.Unmarshal([]byte(payload), &arguments) != nil || arguments == nil {
			return mekugiHistory{}, fmt.Errorf("%s native exec carrier returned invalid arguments", contribution.Name)
		}
		command := jsonString(arguments, "cmd")
		for _, warning := range misuseWarnings {
			misuseWarning += warning + "\n"
		}
		arguments["cmd"] = mustMarshalJSON("printf %s " + shellQuoteArgument(misuseWarning) + "\n" + command)
		payload = string(mustMarshalJSON(arguments))
	} else {
		for _, warning := range misuseWarnings {
			warnedPayload, warningInput, _, warningErr := insertExecCommandWarning(payload, warning)
			if warningErr != nil {
				return mekugiHistory{}, fmt.Errorf("%s interpreter-wrapper warning: %w", contribution.Name, warningErr)
			}
			misuseWarning += warningInput
			payload = warnedPayload
		}
	}

	if contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell" &&
		jsonString(upstreamItem, "name") == t.codeModeToolName && !translation.Rejected {
		payload = misuseWarningProjection(execShellRecoveryWarning) + payload
	}

	if journalToken != "" && !strings.Contains(payload, journalToken) {
		t.proxy.commentary.cancel(journalToken)
		t.commentarySubscriptions = slices.DeleteFunc(t.commentarySubscriptions, func(subscription commentarySubscription) bool { return subscription.token == journalToken })
		journalToken = ""
	}
	history := mekugiHistory{
		ToolName:         contribution.Name,
		PluginID:         contribution.PluginID,
		Script:           input,
		Root:             t.directory,
		CarrierKind:      kind,
		CarrierName:      name,
		CarrierPayload:   payload,
		TranslationError: diagnostic,
		OutputWarning:    outputWarning,
		UpstreamItem:     maps.Clone(upstreamItem),
		ReplayCarrier:    recovered,
	}
	if journalToken != "" {
		history.ShellJournalTurnID = t.shellTurnID
	}
	t.recordLocal(callID, &history)
	return history, nil
}

func mekugiReport(report, diagnostic string) string {
	if diagnostic == "" {
		return report
	}
	if report != "" && !strings.HasSuffix(report, "\n") {
		report += "\n"
	}
	return report + diagnostic
}

func retainedEvaluated(emitted, evaluated string) string {
	if emitted == evaluated {
		return ""
	}
	return evaluated
}
