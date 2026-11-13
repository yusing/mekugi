package inferencegateway

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

// HTTP rejections need the socket's event framing, not a successful response.
// Keep their original status and error so native owns recovery and retry timing.
func webSocketHTTPResponse(response *Response, err error) (*Response, error) {
	if err != nil || response == nil || response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxUpstreamErrorDetailBytes+1))
	if readErr != nil {
		body = []byte(fmt.Sprintf("provider error body could not be read: %v", readErr))
	} else if len(body) > maxUpstreamErrorDetailBytes {
		body = []byte("provider error body exceeds the 8 KiB limit")
	}
	var fields map[string]jsontext.Value
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		fields = make(map[string]jsontext.Value)
	}
	if len(fields["error"]) == 0 {
		fields["error"], err = json.Marshal(map[string]string{"type": "provider_error", "message": strings.ToValidUTF8(string(body), "�")})
		if err != nil {
			return nil, err
		}
	}
	fields["type"], fields["status"] = jsontext.Value(`"error"`), jsontext.Value(strconv.Itoa(response.StatusCode))
	headers := make(map[string]any, len(response.Header))
	var embedded map[string]jsontext.Value
	if json.Unmarshal(fields["headers"], &embedded) == nil {
		for name, value := range embedded {
			headers[strings.ToLower(name)] = value
		}
	}
	for name, values := range response.Header {
		headers[strings.ToLower(name)] = strings.ToValidUTF8(strings.Join(values, ", "), "�")
	}
	fields["headers"], err = json.Marshal(headers)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	var framed bytes.Buffer
	if err := codexresponses.WriteEvent(&framed, payload); err != nil {
		return nil, err
	}
	response.Header = response.Header.Clone()
	response.Header.Del("Content-Length")
	response.Header.Del("Content-Encoding")
	response.Header.Set("Content-Type", "text/event-stream")
	response.Body = io.NopCloser(&framed)
	return response, nil
}
