// Source: capturer/observe.go@b5162407.
package capture

import (
	"bytes"
	"compress/gzip"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"io"
	"slices"
	"strings"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
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

func observeResponse(payload []byte, contentType string, record *captureRecord) {
	if contentType == webSocketContentType || strings.Contains(strings.ToLower(contentType), "text/event-stream") || capturedPayloadLooksLikeSSE(payload) {
		messages := sseData(payload)
		if contentType == webSocketContentType {
			messages = webSocketMessages(payload)
		}
		for data := range messages {
			observeResponseJSON(data, record)
		}
		return
	}
	observeResponseJSON(payload, record)
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

func observeResponseJSON(payload []byte, record *captureRecord) {
	if len(bytes.TrimSpace(payload)) == 0 || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return
	}
	var event struct {
		Type     responseevents.Kind          `json:"type"`
		Response jsonv1.RawMessage            `json:"response"`
		Headers  map[string]jsonv1.RawMessage `json:"headers"`
	}
	if err := json.Unmarshal(payload, &event, jsonv1.DefaultOptionsV1()); err != nil {
		if record.CaptureError == "" {
			record.CaptureError = "invalid response JSON"
		}
		return
	}
	if event.Type == responseevents.Error || event.Type == responseevents.Metadata {
		if record.Boundary == "provider" && record.ProviderResponse != nil {
			for name, raw := range event.Headers {
				var value string
				if json.Unmarshal(raw, &value, jsonv1.DefaultOptionsV1()) != nil {
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
			return
		}
		return
	}
	if len(event.Response) != 0 {
		_, valid := observeResponseEnvelope(event.Response, record)
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
			return
		default:
			return
		}
	}
	status, _ := observeResponseEnvelope(payload, record)
	if responseevents.TerminalStatus(status) {
		record.observeProviderEvidence(payload)
	}
}

func observeResponseEnvelope(payload []byte, record *captureRecord) (string, bool) {
	var response *struct {
		Status string            `json:"status"`
		Model  jsonv1.RawMessage `json:"model"`
		// Validate the envelope shape before accepting terminal provider evidence.
		Output []jsonv1.RawMessage `json:"output"`
	}
	if json.Unmarshal(payload, &response, jsonv1.DefaultOptionsV1()) != nil || response == nil {
		return "", false
	}
	if record.Boundary == "provider" && record.ProviderResponse != nil && len(response.Model) != 0 {
		var model string
		_ = json.Unmarshal(response.Model, &model, jsonv1.DefaultOptionsV1())
		record.ProviderResponse.Model = safeProviderIdentifier(model)
	}
	if response.Status != "" {
		record.ResponseStatus = response.Status
	}
	return response.Status, true
}

func requestToolNames(tools []jsonv1.RawMessage) []string {
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		var tool struct {
			Name     string `json:"name"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &tool, jsonv1.DefaultOptionsV1()) != nil {
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
