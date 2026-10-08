package router

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func codexZstdBody(t *testing.T, body []byte) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	return encoder.EncodeAll(body, nil)
}

func TestResponsesHandlerDecodesCodexHTTPBody(t *testing.T) {
	original := []byte(" {\"model\":\"gpt-test\",\"input\":\"hello\",\"access_programs\":{\"cyber\":\"standard\"}}\n")
	for _, codec := range []string{"", "identity", "zstd"} {
		t.Run("codec="+codec, func(t *testing.T) {
			body := original
			if codec == "zstd" {
				body = codexZstdBody(t, original)
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
			request := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
			request.Header.Set("Content-Encoding", codec)
			recorder := httptest.NewRecorder()
			responsesHandler(t.Context(), time.Minute, provider, nil, nil)(recorder, request)
			if recorder.Code != http.StatusOK || len(provider.forwarded) != 1 {
				t.Fatalf("status=%d forwards=%d body=%q", recorder.Code, len(provider.forwarded), recorder.Body.String())
			}
			if !bytes.Equal(provider.forwarded[0], original) {
				t.Fatalf("forwarded=%q, want original decoded JSON %q", provider.forwarded[0], original)
			}
		})
	}
}

func TestResponsesHandlerRejectsInvalidOrOversizedCodexHTTPBody(t *testing.T) {
	for _, test := range []struct {
		name, codec string
		body        func(*testing.T) io.Reader
		status      int
	}{
		{"unsupported codec", "gzip", func(*testing.T) io.Reader { return bytes.NewBufferString(`{"model":"gpt-test","input":"hello"}`) }, http.StatusUnsupportedMediaType},
		{"invalid zstd", "zstd", func(*testing.T) io.Reader { return bytes.NewBufferString("not a zstd frame") }, http.StatusBadRequest},
		{"encoded budget", "zstd", func(*testing.T) io.Reader {
			return io.LimitReader(serverRepeatingReader{}, responsesRequestBufferBytes+1)
		}, http.StatusRequestEntityTooLarge},
		{"decoded budget", "zstd", func(t *testing.T) io.Reader {
			return bytes.NewReader(codexZstdBody(t, bytes.Repeat([]byte(" "), responsesRequestBufferBytes+1)))
		}, http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &serverFakeProvider{}
			request := httptest.NewRequest("POST", "/v1/responses", test.body(t))
			request.Header.Set("Content-Encoding", test.codec)
			recorder := httptest.NewRecorder()
			responsesHandler(t.Context(), time.Minute, provider, nil, nil)(recorder, request)
			if recorder.Code != test.status || len(provider.forwarded) != 0 {
				t.Fatalf("status=%d forwards=%d, want %d and none; body=%q", recorder.Code, len(provider.forwarded), test.status, recorder.Body.String())
			}
		})
	}
}
