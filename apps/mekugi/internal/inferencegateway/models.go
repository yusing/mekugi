package inferencegateway

// Source: apps/mekugi/internal/router/server.go:457:551@[e5c6fadc] catalog transport.
import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// MaxCatalogBytes bounds the single complete cached catalog.
const MaxCatalogBytes = 8 << 20

// CatalogFailure is a failed transport's facts for private application diagnostics.
// An empty Code leaves transport-error classification with the consumer.
type CatalogFailure struct {
	Err                              error
	Code, Summary                    string
	UpstreamStatus, DownstreamStatus int
}

func ModelsHandler(provider *Client, report func(*http.Request, CatalogFailure)) http.HandlerFunc {
	// Session-local, single-entry cache. The gate coalesces concurrent refreshes
	// without keeping canceled callers waiting for an upstream operation.
	gate := make(chan struct{}, 1)
	var cached *modelsCacheEntry
	return func(writer http.ResponseWriter, request *http.Request) {
		tracked := &ResponseWriter{ResponseWriter: writer}
		writer = tracked
		var upstreamStatus int
		var failure CatalogFailure
		defer func() {
			if tracked.StatusCode() >= 400 && request.Context().Err() == nil && report != nil {
				if failure.Err == nil && failure.Code == "" {
					failure.Code = fmt.Sprintf("models_http_%d", upstreamStatus)
					failure.Summary = fmt.Sprintf("the upstream model catalog returned HTTP %d", upstreamStatus)
				}
				failure.UpstreamStatus, failure.DownstreamStatus = upstreamStatus, tracked.StatusCode()
				report(request, failure)
			}
		}()

		release := func() {}
		key, cacheable := modelsRequestCacheKey(request)
		if cacheable {
			select {
			case gate <- struct{}{}:
				release = sync.OnceFunc(func() { <-gate })
				defer release()
			case <-request.Context().Done():
				return
			}
			if request.Context().Err() != nil {
				return
			}
			if cached != nil && cached.key == key {
				entry := cached
				release()
				writeModelsResponse(writer, http.StatusOK, entry.headers, entry.body)
				return
			}
		}

		response, err := provider.ForwardModels(request.Context(), request.Header, request.URL.RawQuery)
		if err != nil {
			failure = CatalogFailure{Err: err}
			release()
			http.Error(writer, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		upstreamStatus = response.StatusCode
		body, err := io.ReadAll(io.LimitReader(response.Body, MaxCatalogBytes+1))
		if err != nil {
			failure = CatalogFailure{Err: err, Code: "models_body_read", Summary: "the upstream model catalog response could not be read"}
			release()
			http.Error(writer, fmt.Sprintf("read upstream models response: %v", err), http.StatusBadGateway)
			return
		}
		if len(body) > MaxCatalogBytes {
			failure = CatalogFailure{Code: "models_body_limit", Summary: "the upstream model catalog response exceeded the router buffer budget"}
			release()
			http.Error(writer, "upstream models response exceeds the router buffer budget", http.StatusBadGateway)
			return
		}
		if response.StatusCode >= http.StatusBadRequest {
			cause := fmt.Sprintf("model catalog upstream returned HTTP %d", response.StatusCode)
			if len(body) != 0 {
				cause += ": " + string(body)
			}
			failure = CatalogFailure{Err: errors.New(cause), Code: fmt.Sprintf("models_http_%d", response.StatusCode), Summary: fmt.Sprintf("the upstream model catalog returned HTTP %d", response.StatusCode)}
		}
		if cacheable && response.StatusCode == http.StatusOK {
			headers := make(http.Header)
			for _, name := range []string{"Content-Type", "Cache-Control", "ETag"} {
				for _, value := range response.Header.Values(name) {
					headers.Add(name, value)
				}
			}
			cached = &modelsCacheEntry{key: key, headers: headers, body: body}
		}
		release()
		writeModelsResponse(writer, response.StatusCode, response.Header, body)
	}
}
