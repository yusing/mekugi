package router

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
