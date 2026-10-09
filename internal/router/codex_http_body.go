package router

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func readCodexHTTPBody(request *http.Request) ([]byte, int, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, responsesRequestBufferBytes+1))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("read Codex request: %w", err)
	}
	if len(body) > responsesRequestBufferBytes {
		return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("Codex request exceeds the router buffer budget")
	}
	switch strings.TrimSpace(strings.ToLower(request.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "zstd":
		decoder, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(responsesRequestBufferBytes))
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("decode Codex request: %w", err)
		}
		defer decoder.Close()
		body, err = io.ReadAll(io.LimitReader(decoder, responsesRequestBufferBytes+1))
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("decode Codex request: %w", err)
		}
		if len(body) > responsesRequestBufferBytes {
			return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("decoded Codex request exceeds the router buffer budget")
		}
	default:
		return nil, http.StatusUnsupportedMediaType, fmt.Errorf("unsupported Codex Content-Encoding")
	}
	return body, http.StatusOK, nil
}
