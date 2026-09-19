package capturer

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/commentaryid"
	responseevents "github.com/yusing/mekugi/internal/responses"
	"github.com/yusing/mekugi/internal/tokenizer"
)

var errDecodedPayloadTooLarge = errors.New("decoded response exceeds capture observation limit")

func decodedCapturePayload(payload []byte, contentEncoding string) ([]byte, error) {
	switch strings.TrimSpace(strings.ToLower(contentEncoding)) {
	case "":
		return payload, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		decompressed, readErr := io.ReadAll(io.LimitReader(reader, maxObservedResponseBytes+1))
		closeErr := reader.Close()
		if len(decompressed) > maxObservedResponseBytes {
			return nil, errors.Join(errDecodedPayloadTooLarge, readErr, closeErr)
		}
		return decompressed, errors.Join(readErr, closeErr)
	default:
		return nil, errors.New("unsupported content encoding")
	}
}

func observeResponse(payload []byte, contentType string, record *captureRecord, codec tokenizer.Codec) []byte {
	if contentType == webSocketContentType || strings.Contains(strings.ToLower(contentType), "text/event-stream") || capturedPayloadLooksLikeSSE(payload) {
		var finalOutput []byte
		terminalOutputObserved := false
		completedItems := make(map[int]json.RawMessage)
		messages := sseData(payload)
		if contentType == webSocketContentType {
			messages = webSocketMessages(payload)
		}
		for data := range messages {
			if index, item, ok := completedResponseOutputItem(data); ok && !generatedOutputItem(item, record) {
				completedItems[index] = item
			}
			if output, terminal := observeResponseJSON(data, record, codec); terminal {
				finalOutput = output
				terminalOutputObserved = true
			}
		}
		if !terminalOutputObserved {
			return nil
		}
		var terminalItems []json.RawMessage
		if len(finalOutput) != 0 && (json.Unmarshal(finalOutput, &terminalItems) != nil || len(terminalItems) != 0) {
			return finalOutput
		}
		if len(completedItems) == 0 {
			return finalOutput
		}
		indices := slices.Sorted(maps.Keys(completedItems))
		items := make([]json.RawMessage, 0, len(indices))
		for _, index := range indices {
			items = append(items, completedItems[index])
		}
		output, err := json.Marshal(items)
		if err != nil {
			if record.CaptureError == "" {
				record.CaptureError = "encode completed response output"
			}
			return nil
		}
		return output
	}
	output, _ := observeResponseJSON(payload, record, codec)
	return output
}

func completedResponseOutputItem(payload []byte) (int, json.RawMessage, bool) {
	var event struct {
		Type        responseevents.Kind `json:"type"`
		OutputIndex *int                `json:"output_index"`
		Item        json.RawMessage     `json:"item"`
	}
	if json.Unmarshal(payload, &event) != nil || event.Type != responseevents.OutputItemDone ||
		event.OutputIndex == nil || *event.OutputIndex < 0 || len(event.Item) == 0 {
		return 0, nil, false
	}
	return *event.OutputIndex, event.Item, true
}

func capturedPayloadLooksLikeSSE(payload []byte) bool {
	for line := range sseLines(payload) {
		if len(line) == 0 {
			continue
		}
		if line[0] == ':' {
			return true
		}
		field, _, _ := bytes.Cut(line, []byte{':'})
		return bytes.Equal(field, []byte("data")) || bytes.Equal(field, []byte("event")) ||
			bytes.Equal(field, []byte("id")) || bytes.Equal(field, []byte("retry"))
	}
	return false
}

func observeResponseJSON(payload []byte, record *captureRecord, codec tokenizer.Codec) ([]byte, bool) {
	if len(bytes.TrimSpace(payload)) == 0 || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return nil, false
	}
	var event struct {
		Type     responseevents.Kind        `json:"type"`
		Item     json.RawMessage            `json:"item"`
		Response json.RawMessage            `json:"response"`
		Headers  map[string]json.RawMessage `json:"headers"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		if record.CaptureError == "" {
			record.CaptureError = "invalid response JSON"
		}
		return nil, false
	}
	if event.Type == responseevents.Error || event.Type == responseevents.Metadata {
		if record.Boundary == "provider" && record.ProviderResponse != nil {
			for name, raw := range event.Headers {
				var value string
				if json.Unmarshal(raw, &value) != nil {
					continue
				}
				switch strings.ToLower(name) {
				case "x-request-id":
					record.ProviderResponse.RequestID = safeProviderIdentifier(value)
				case "openai-model":
					record.ProviderResponse.HeaderModel = safeProviderIdentifier(value)
				}
			}
		}
		if event.Type == responseevents.Error {
			record.ResponseStatus = responseevents.Error
			return nil, true
		}
		return nil, false
	}
	if len(event.Item) != 0 {
		observeOutputItem(event.Item, record, codec)
	}
	if len(event.Response) != 0 {
		_, output, valid := observeResponseEnvelope(event.Response, record, codec)
		switch {
		case event.Type.Terminal():
			if valid {
				record.ResponseStatus = event.Type.Status()
				record.observeProviderEvidence(event.Response)
			} else if record.ProviderResponse != nil {
				record.ProviderResponse.CachedTokensState = "unavailable"
				record.ProviderResponse.CachedTokens = nil
			}
			return output, true
		default:
			return nil, false
		}
	}
	status, output, _ := observeResponseEnvelope(payload, record, codec)
	switch {
	case responseevents.TerminalStatus(status):
		record.observeProviderEvidence(payload)
		return output, true
	default:
		return nil, false
	}
}

func observeResponseEnvelope(payload []byte, record *captureRecord, codec tokenizer.Codec) (string, []byte, bool) {
	var response *struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Model  json.RawMessage   `json:"model"`
	}
	if json.Unmarshal(payload, &response) != nil || response == nil {
		return "", nil, false
	}
	if record.Boundary == "provider" && record.ProviderResponse != nil && len(response.Model) != 0 {
		var model string
		_ = json.Unmarshal(response.Model, &model)
		record.ProviderResponse.Model = safeProviderIdentifier(model)
	}
	if response.Status != "" {
		record.ResponseStatus = response.Status
	}
	for _, item := range response.Output {
		observeOutputItem(item, record, codec)
	}
	if response.Output == nil {
		return response.Status, nil, true
	}
	// Remove only router-origin messages, before deciding whether a terminal
	// array can replace the indexed streamed items. Telemetry alone is not a
	// complete model output. Transport measurement still sees every byte.
	outputItems := slices.DeleteFunc(response.Output, func(item json.RawMessage) bool {
		return generatedOutputItem(item, record)
	})
	output, err := json.Marshal(outputItems)
	if err != nil {
		return "", nil, false
	}
	return response.Status, output, true
}

func generatedOutputItem(payload []byte, record *captureRecord) bool {
	if record.Boundary != "codex" || record.Mode != "mekugi" {
		return false
	}
	var item struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	return json.Unmarshal(payload, &item) == nil && item.Type == "message" && commentaryid.Generated(item.ID)
}

func observeOutputItem(payload []byte, record *captureRecord, codec tokenizer.Codec) {
	var item struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Input     string `json:"input"`
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(payload, &item) != nil || !responseevents.ItemKind(item.Type).ToolCall() {
		return
	}
	callID := cmp.Or(item.CallID, item.ID)
	input := cmp.Or(item.Input, item.Arguments)
	inputTokens, inputErr := codec.Count(input)
	itemTokens, itemErr := contentTokens(payload, codec)
	if inputErr != nil || itemErr != nil || inputTokens < 0 || itemTokens < 0 || callID == "" || item.Name == "" {
		return
	}
	metric := toolCallMetrics{
		CallID: callID, Name: item.Name,
		InputBytes: uint64(len(input)), InputTokens: uint64(inputTokens),
		ItemBytes: uint64(len(payload)), ItemTokens: uint64(itemTokens),
	}
	metric.Kind, metric.Diagnostic = classifyToolInput(item.Name, input)
	for index := range record.ToolCalls {
		if record.ToolCalls[index].CallID == callID {
			if metric.InputBytes != 0 || record.ToolCalls[index].InputBytes == 0 {
				record.ToolCalls[index] = metric
			}
			return
		}
	}
	record.ToolCalls = append(record.ToolCalls, metric)
}

func classifyToolInput(name, input string) (string, string) {
	if name == "exec_command" {
		var arguments struct {
			Command string `json:"cmd"`
		}
		if json.Unmarshal([]byte(input), &arguments) != nil {
			return "", ""
		}
		switch {
		case strings.HasPrefix(arguments.Command, mekugiNativeApplyCarrierPrefix):
			return "apply_patch", ""
		case strings.HasPrefix(arguments.Command, mekugiNativeReportCarrierPrefix):
			return "mekugi_report", ""
		case strings.HasPrefix(arguments.Command, mekugiNativeDiagnosticCarrierPrefix):
			line, _, _ := strings.Cut(arguments.Command, "\n")
			encoded := strings.TrimPrefix(line, mekugiNativeDiagnosticCarrierPrefix)
			diagnostic, err := strconv.Unquote(encoded)
			if err != nil {
				return "mekugi_diagnostic", ""
			}
			return "mekugi_diagnostic", mekugiDiagnosticCode(diagnostic)
		default:
			return "", ""
		}
	}
	if name != "exec" {
		return "", ""
	}
	if first, _, ok := strings.Cut(input, "\n"); ok {
		if encoded, ok := strings.CutPrefix(first, "text("); ok {
			if encoded, ok := strings.CutSuffix(encoded, ");"); ok {
				if warning, err := strconv.Unquote(encoded); err == nil && strings.HasPrefix(warning, "shell: [shell-code-mode-recovered] ") {
					return "code_mode_recovery", "shell-code-mode-recovered"
				}
			}
		}
	}
	switch {
	case strings.HasPrefix(input, mekugiApplyCarrierPrefix):
		return "apply_patch", ""
	case strings.HasPrefix(input, "const result = await tools.exec_command("):
		return "exec_command", ""
	}
	encoded, ok := strings.CutPrefix(input, "text(")
	if !ok {
		return "other", ""
	}
	encoded, ok = strings.CutSuffix(encoded, ");")
	if !ok {
		return "other", ""
	}
	text, err := strconv.Unquote(encoded)
	if err != nil {
		return "other", ""
	}
	report := withoutChangeNotice(text)
	if (strings.HasPrefix(report, "file ") || strings.HasPrefix(report, "in ")) && strings.Contains(report, "\nlast ") && strings.Contains(report, "\nfiles ") {
		return "mekugi_report", ""
	}
	if code := mekugiDiagnosticCode(text); code != "" {
		return "mekugi_diagnostic", code
	}
	return "other", ""
}

// A change notice precedes both reports and rejection diagnostics. It is not
// outcome evidence on its own; the remaining envelope must still be recognized.
func withoutChangeNotice(text string) string {
	line, rest, newline := strings.Cut(text, "\n")
	if id, notice := strings.CutPrefix(line, "change "); newline && notice && id != "" && !strings.ContainsAny(id, " \t\r") {
		return rest
	}
	return text
}

func mekugiDiagnosticCode(text string) string {
	text = withoutChangeNotice(text)
	if strings.HasPrefix(text, "shell: [shell-typescript-misuse] ") {
		return "shell-typescript-misuse"
	}
	line, _, _ := strings.Cut(text, "\n")
	command, reason, ok := strings.Cut(line, ", reason ")
	if !ok || !strings.Contains(command, ": command ") {
		return ""
	}
	code, _, ok := strings.Cut(reason, ":")
	if !ok {
		return ""
	}
	switch code {
	case "script-syntax", "row-missing", "row-stale", "occurrence-missing", "invalid-count",
		"target-order", "edit-conflict", "active-file", "initialization", "file-path",
		"language-syntax", "other":
		return code
	default:
		return ""
	}
}

func requestToolNames(tools []json.RawMessage) []string {
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		var tool struct {
			Name     string `json:"name"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &tool) != nil {
			continue
		}
		if tool.Name == "" {
			tool.Name = tool.Function.Name
		}
		if tool.Name != "" && !slices.Contains(names, tool.Name) {
			names = append(names, tool.Name)
		}
	}
	slices.Sort(names)
	return names
}
