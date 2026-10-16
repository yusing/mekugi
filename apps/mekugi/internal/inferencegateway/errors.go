package inferencegateway

// Source: apps/mekugi/internal/router/provider_http_error.go:1:106@[bd606c09] provider errors.

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
)

// Provider details are included in the caller-facing error and failure records.
type ProviderHTTPError struct {
	Status int
	label  string
	detail string
	raw    string
}

func (e *ProviderHTTPError) Error() string {
	message := e.label + ": " + e.detail
	if e.Status != 0 {
		message = fmt.Sprintf("%s returned HTTP %d: %s", e.label, e.Status, e.detail)
	}
	if e.raw != "" && e.raw != e.detail {
		message += "\nRaw response: " + e.raw
	}
	return message
}

func NewProviderHTTPError(label string, status int, body []byte) error {
	raw := string(body)
	detail := strings.TrimSpace(string(body))
	var envelope struct {
		Error    jsontext.Value `json:"error"`
		Response jsontext.Value `json:"response"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		if len(envelope.Response) != 0 && string(envelope.Response) != "null" {
			body = envelope.Response
			envelope.Error = nil
			_ = json.Unmarshal(body, &envelope)
		}
		value := body
		if len(envelope.Error) != 0 && string(envelope.Error) != "null" {
			value = envelope.Error
		}
		var message string
		if json.Unmarshal(value, &message) == nil {
			detail = message
		} else {
			var fields struct {
				Name    string `json:"name"`
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if json.Unmarshal(value, &fields) == nil && fields.Message != "" {
				detail = fields.Message
				for _, kind := range []string{fields.Name, fields.Type, fields.Code} {
					if kind != "" && kind != "error" {
						detail = kind + ": " + detail
						break
					}
				}
			}
		}
	}
	if detail == "" {
		detail = http.StatusText(status)
	}
	return &ProviderHTTPError{Status: status, label: label, detail: detail, raw: raw}
}

// TerminalProviderError extracts caller-facing details without exporting them
// through sanitized stream diagnostics or changing delivery of the original event.
func TerminalProviderError(payload []byte, stream bool) error {
	var envelope struct {
		Status   int            `json:"status"`
		Response jsontext.Value `json:"response"`
	}
	// A non-stream response uses a string status, not an HTTP status.
	body := payload
	status := 0
	if stream {
		if json.Unmarshal(payload, &envelope) != nil {
			return nil
		}
		if envelope.Status >= 400 && envelope.Status <= 599 {
			status = envelope.Status
		}
		if len(envelope.Response) != 0 && string(envelope.Response) != "null" {
			body = envelope.Response
		}
	}
	var detail struct {
		Error   jsontext.Value `json:"error"`
		Message string         `json:"message"`
	}
	if json.Unmarshal(body, &detail) != nil {
		return nil
	}
	if (len(detail.Error) == 0 || string(detail.Error) == "null") && detail.Message == "" {
		return nil
	}
	return NewProviderHTTPError("Upstream response failed", status, body)
}

// CompatibilityError rejects an unsupported model request before inference.
type CompatibilityError struct{ Code, Message string }

func (e *CompatibilityError) Error() string { return "Mekugi " + e.Code + ": " + e.Message }
func IncompatibleRequest(code, message string) error {
	return &CompatibilityError{Code: code, Message: message}
}
