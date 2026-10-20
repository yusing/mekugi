package router

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// These are Codex-owned backend operations, not Responses requests. In
// particular, note writes must never acquire automatic router retries.
var codexAuxiliaryPaths = []string{
	"alpha/search",
	"images/generations",
	"images/edits",
	"alpha/history/v2/list_windows",
	"alpha/history/v2/list_items",
	"alpha/history/v2/read_item",
	"alpha/history/v2/search_contents",
	"alpha/notes/v2/list_files_by_prefix",
	"alpha/notes/v2/read_file",
	"alpha/notes/v2/search_contents",
	"alpha/notes/v2/append_to_file",
	"alpha/notes/v2/write_file",
	"alpha/notes/v2/thread_hint",
}

func registerCodexAuxiliaryRoutes(mux *http.ServeMux, lifecycle context.Context, responseStartTimeout time.Duration, provider *providerClient) {
	if provider.thirdPartyOnly {
		return
	}
	client := *provider.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, path := range codexAuxiliaryPaths {
		mux.HandleFunc("POST /v1/"+path, func(writer http.ResponseWriter, request *http.Request) {
			authorization, account, err := requiredCodexAuthHeaders(request.Header)
			if err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			startCtx, executionCtx, cancel := requestContexts(request.Context(), lifecycle, responseStartTimeout)
			defer cancel()
			ctx, cancelUpstream := context.WithCancel(executionCtx)
			defer cancelUpstream()
			stopStart := context.AfterFunc(startCtx, cancelUpstream)
			defer stopStart()
			upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.baseURL, "/")+"/"+path, request.Body)
			if err != nil {
				http.Error(writer, "build Codex auxiliary request", http.StatusBadGateway)
				return
			}
			upstream.URL.RawQuery = request.URL.RawQuery
			upstream.ContentLength = request.ContentLength
			upstream.Header.Set("Authorization", authorization)
			upstream.Header.Set(chatGPTAccountIDHeader, account)
			upstream.Header.Set("Content-Type", request.Header.Get("Content-Type"))
			if encoding := request.Header.Get("Content-Encoding"); encoding != "" {
				upstream.Header.Set("Content-Encoding", encoding)
			}
			upstream.Header.Set("Accept", "application/json")
			upstream.Header.Set("Originator", codexClientIdentity)
			upstream.Header.Set("User-Agent", codexClientIdentity)
			forwardCodexRequestHeaders(upstream.Header, request.Header)
			for _, name := range []string{"x-codex-image-turn-id", "x-openai-encrypted-tool-arguments", "x-openai-tool-output-truncation-policy"} {
				for _, value := range request.Header.Values(name) {
					upstream.Header.Add(name, value)
				}
			}
			response, err := client.Do(upstream)
			if err != nil {
				http.Error(writer, "forward Codex auxiliary request", http.StatusBadGateway)
				return
			}
			defer response.Body.Close()
			if !stopStart() {
				http.Error(writer, "Codex auxiliary request timed out or was canceled", http.StatusBadGateway)
				return
			}
			if provider.streamIdleTimeout > 0 {
				response.Body = newStreamIdleReadCloser(ctx, response.Body, provider.streamIdleTimeout)
			}
			copyAuxiliaryResponseHeaders(writer.Header(), response.Header)
			writer.WriteHeader(response.StatusCode)
			if _, err := io.Copy(writer, response.Body); err != nil {
				// A partial result must not look like a complete successful body.
				panic(http.ErrAbortHandler)
			}
		})
	}
}

func copyAuxiliaryResponseHeaders(destination, source http.Header) {
	header := source.Clone()
	for _, connection := range header.Values("Connection") {
		for _, name := range strings.Split(connection, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
	for name, values := range header {
		destination[name] = values
	}
}
