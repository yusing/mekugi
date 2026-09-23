package router

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const nativeExecCommandToolName = "exec_command"

// Carrier fields remain in retained history because journal calls and observed
// stock calls may be custom or function tools. Mekugi no longer constructs a
// substitute execution carrier for stock editing or execution.
type codeModeCarrierKind string

const (
	codeModeCarrierCustom   codeModeCarrierKind = "custom"
	codeModeCarrierFunction codeModeCarrierKind = "function"
)

func carrierItemType(kind codeModeCarrierKind) string {
	if kind == codeModeCarrierFunction {
		return "function_call"
	}
	return "custom_tool_call"
}

func carrierOutputItemType(kind codeModeCarrierKind) string {
	if kind == codeModeCarrierFunction {
		return "function_call_output"
	}
	return "custom_tool_call_output"
}

func carrierPayloadField(kind codeModeCarrierKind) string {
	if kind == codeModeCarrierFunction {
		return "arguments"
	}
	return "input"
}

func (h mekugiHistory) effectiveCarrierKind() codeModeCarrierKind {
	if h.CarrierKind != "" {
		return h.CarrierKind
	}
	return codeModeCarrierCustom
}

func (h mekugiHistory) carrierInput() string { return h.CarrierPayload }

func shellQuoteArgument(value string) string {
	quoted, err := syntax.Quote(value, syntax.LangBash)
	if err == nil {
		return quoted
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func workerCommand(executable string, arguments []string) string {
	var command strings.Builder
	command.WriteString(shellQuoteArgument(executable))
	for _, argument := range arguments {
		command.WriteByte(' ')
		command.WriteString(shellQuoteArgument(argument))
	}
	return command.String()
}
