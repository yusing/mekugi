package inferencegateway

import "net/http"

// ResponseWriter tracks delivery admission and classifies downstream failures.
// Source: apps/mekugi/internal/router/server.go:582:633@b9679c93.
type ResponseWriter struct {
	http.ResponseWriter

	committed  bool
	statusCode int
}

func (w *ResponseWriter) WriteHeader(statusCode int) {
	if w.committed {
		return
	}
	w.committed = true
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *ResponseWriter) Write(body []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	return n, DownstreamDisconnectError(err)
}

func (w *ResponseWriter) FlushError() error {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return DownstreamDisconnectError(http.NewResponseController(w.ResponseWriter).Flush())
}

func (w *ResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *ResponseWriter) ResponseStarted() bool { return w.committed }
func (w *ResponseWriter) StatusCode() int       { return w.statusCode }
