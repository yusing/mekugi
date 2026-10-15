package capturer

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/commentaryid"
	responseevents "github.com/yusing/mekugi/internal/responses"
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

func observeResponse(payload []byte, contentType string, record *captureRecord) []byte {
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
			if output, terminal := observeResponseJSON(data, record); terminal {
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
	output, _ := observeResponseJSON(payload, record)
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

func observeResponseJSON(payload []byte, record *captureRecord) ([]byte, bool) {
	if len(bytes.TrimSpace(payload)) == 0 || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return nil, false
	}
	var event struct {
		Type     responseevents.Kind        `json:"type"`
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
	if len(event.Response) != 0 {
		_, output, valid := observeResponseEnvelope(event.Response, record)
		switch {
		case event.Type.Terminal():
			if valid {
				record.ResponseStatus = event.Type.Status()
				record.observeProviderEvidence(event.Response)
			} else if record.ProviderResponse != nil {
				record.ProviderResponse.ServiceTier = ""
				record.ProviderResponse.CachedTokensState = "unavailable"
				record.ProviderResponse.CachedTokens = nil
			}
			return output, true
		default:
			return nil, false
		}
	}
	status, output, _ := observeResponseEnvelope(payload, record)
	switch {
	case responseevents.TerminalStatus(status):
		record.observeProviderEvidence(payload)
		return output, true
	default:
		return nil, false
	}
}

func observeResponseEnvelope(payload []byte, record *captureRecord) (string, []byte, bool) {
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
