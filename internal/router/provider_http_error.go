package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// Provider details belong in caller-facing errors, not sanitized diagnostics.
type providerHTTPError struct {
	status int
	label  string
	detail string
}

func (e *providerHTTPError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d: %s", e.label, e.status, e.detail)
}

func newProviderHTTPError(label string, status int, body []byte, headers ...http.Header) error {
	detail := strings.TrimSpace(string(body))
	var envelope struct {
		Error jsontext.Value `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil {
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
					if kind != "" {
						detail = kind + ": " + detail
						break
					}
				}
			}
		}
	}
	// Scrub both provider and caller credentials before bounding display text.
	for _, header := range headers {
		for _, name := range []string{"Authorization", "x-api-key", chatGPTAccountIDHeader} {
			for _, value := range header.Values(name) {
				secret := strings.TrimSpace(value)
				if strings.EqualFold(name, "Authorization") {
					if scheme, token, ok := strings.Cut(secret, " "); ok && strings.EqualFold(scheme, "Bearer") {
						secret = strings.TrimSpace(token)
					}
				}
				if secret != "" {
					detail = strings.ReplaceAll(detail, secret, "[redacted]")
				}
			}
		}
	}
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, detail)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = http.StatusText(status)
	}
	if runes := []rune(detail); len(runes) > 2048 {
		detail = string(runes[:2048]) + " [truncated]"
	}
	return &providerHTTPError{status: status, label: label, detail: detail}
}
