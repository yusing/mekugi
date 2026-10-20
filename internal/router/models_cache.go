package router

import (
	"crypto/sha256"
	"net/http"
)

// A handler retains only its latest catalog, bounding retention to one response.
// Credentials are validated before lookup and retained only as a digest.
type modelsCacheKey struct {
	authorization [sha256.Size]byte
	account       string
	session       string
	query         string
}

type modelsCacheEntry struct {
	key     modelsCacheKey
	headers http.Header
	body    []byte
}

func modelsRequestCacheKey(request *http.Request) (modelsCacheKey, bool) {
	authorization, account, err := requiredCodexAuthHeaders(request.Header)
	if err != nil {
		return modelsCacheKey{}, false
	}
	return modelsCacheKey{
		authorization: sha256.Sum256([]byte(authorization)),
		account:       account,
		session:       request.Header.Get(sessionIDHeader),
		query:         request.URL.RawQuery,
	}, true
}

func writeModelsResponse(writer http.ResponseWriter, status int, headers http.Header, body []byte) {
	for _, name := range []string{"Content-Type", "Cache-Control", "ETag"} {
		for _, value := range headers.Values(name) {
			writer.Header().Add(name, value)
		}
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}
