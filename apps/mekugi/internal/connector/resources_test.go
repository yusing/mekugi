package connector

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func TestConnectorResourceAdmissionAndForwarding(t *testing.T) {
	var calls atomic.Int32
	socket, _ := upstream(t, sessionapi.Versioned(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		for _, header := range []string{"Authorization", "Cookie", "Origin", "X-Mekugi-Authority"} {
			if r.Header.Get(header) != "" {
				t.Errorf("forwarded caller %s", header)
			}
		}
		data, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != sessionapi.ResourcePath+"image" || string(data) != `{"thread":"selected","body":{"image":"bound"}}` {
			t.Error("resource request changed")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{1, 2, 3})
	})))
	connector, tokenFile := connect(t, socket, "https://ui.test")
	token := readToken(t, tokenFile)
	for _, tc := range []struct {
		name, method, path, origin, credential, version string
		status                                          int
	}{
		{"preflight", "OPTIONS", "image", "https://ui.test", "", "", 204},
		{"image", "POST", "image", "https://ui.test", token, sessionapi.ProtocolVersion, 200},
		{"non-browser", "POST", "image", "", token, sessionapi.ProtocolVersion, 200},
		{"no token", "POST", "image", "https://ui.test", "", sessionapi.ProtocolVersion, 401},
		{"bad token", "POST", "image", "https://ui.test", "wrong", sessionapi.ProtocolVersion, 401},
		{"bad origin", "POST", "image", "https://other.test", token, sessionapi.ProtocolVersion, 403},
		{"bad preflight", "OPTIONS", "image", "https://other.test", "", "", 403},
		{"version", "POST", "image", "https://ui.test", token, "wrong", 409},
		{"query credential", "POST", "image?token=x", "https://ui.test", token, sessionapi.ProtocolVersion, 400},
		{"GET", "GET", "image", "https://ui.test", token, sessionapi.ProtocolVersion, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			request := httptest.NewRequest(tc.method, sessionapi.ResourcePath+tc.path, strings.NewReader(`{"thread":"selected","body":{"image":"bound"}}`))
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			if tc.credential != "" {
				request.Header.Set("Authorization", "Bearer "+tc.credential)
			}
			request.Header.Set(sessionapi.ProtocolHeader, tc.version)
			request.Header.Set("Cookie", "ignored=secret")
			request.Header.Set("X-Mekugi-Authority", "native")
			response := httptest.NewRecorder()
			connector.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status %d: %s", response.Code, response.Body)
			}
			if tc.status == 200 {
				if response.Header().Get("Content-Type") != "image/png" || response.Body.String() != string([]byte{1, 2, 3}) {
					t.Fatal("resource changed")
				}
			} else if calls.Load() != before {
				t.Fatal("refused request reached resource owner")
			}
			if tc.origin == "https://ui.test" && tc.status != 400 && response.Header().Get("Access-Control-Allow-Origin") != tc.origin {
				t.Fatal("missing admitted origin")
			}
		})
	}
}

func TestConnectorRotationCancelsResourceTransfers(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	socket, _ := upstream(t, sessionapi.Versioned(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
	})))
	connector, tokenFile := connect(t, socket, "https://ui.test")
	token := readToken(t, tokenFile)
	remote := httptest.NewServer(connector)
	defer remote.Close()
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, remote.URL+sessionapi.ResourcePath+"image", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(sessionapi.ProtocolHeader, sessionapi.ProtocolVersion)
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := remote.Client().Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	<-started
	if err := connector.Rotate(); err != nil {
		t.Fatal(err)
	}
	<-canceled
	<-done
	request = httptest.NewRequest(http.MethodPost, sessionapi.ResourcePath+"image", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	connector.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("old resource credential survived rotation")
	}
}

func TestConnectorTruncatedResourceFails(t *testing.T) {
	socket, _ := upstream(t, sessionapi.Versioned(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("partial"))
	})))
	c, tokenFile := connect(t, socket)
	remote := httptest.NewServer(c)
	defer remote.Close()
	request, _ := http.NewRequestWithContext(t.Context(), "POST", remote.URL+sessionapi.ResourcePath+"image", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+readToken(t, tokenFile))
	request.Header.Set(sessionapi.ProtocolHeader, sessionapi.ProtocolVersion)
	response, err := remote.Client().Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err == nil && response.StatusCode == 200 {
		t.Fatalf("truncated upstream returned success: status=%d length=%d body=%q", response.StatusCode, response.ContentLength, data)
	}
}

func TestConnectorRotationRetiresIncompleteResourceBody(t *testing.T) {
	started := make(chan struct{})
	socket, _ := upstream(t, sessionapi.Versioned(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		_, _ = io.Copy(io.Discard, r.Body)
	})))
	c, tokenFile := connect(t, socket)
	remote := httptest.NewServer(c)
	defer remote.Close()
	conn, err := net.Dial("tcp", remote.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = io.WriteString(conn, "POST /api/resources/image HTTP/1.1\r\nHost: fixture\r\nAuthorization: Bearer "+readToken(t, tokenFile)+"\r\nMekugi-Protocol: "+sessionapi.ProtocolVersion+"\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := c.Rotate(); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _ = c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		conn.Close()
		<-closed
		t.Fatal("rotation/Close waits for the old caller to finish its request body")
	}
}

func TestConnectorResourceLimitsRetainSemanticStatus(t *testing.T) {
	hub := sessionapi.NewHub(t.Context(), nil)
	defer hub.Close()
	socket, _ := upstream(t, sessionapi.Versioned(hub.Handler()))
	connector, tokenFile := connect(t, socket)
	remote := httptest.NewServer(connector)
	defer remote.Close()
	for _, tc := range []struct {
		kind, body string
		status     int
	}{
		{"attachment-image", `{"body":{"path":"` + strings.Repeat("x", 200<<10) + `"}}`, 413},
		{"attachment-image", `{"body":{"path":"` + strings.Repeat("x", 70<<10) + `"}}`, 413},
		{"content", `{"body":{"padding":"` + strings.Repeat("x", 2<<10) + `"}}`, 413},
		{"content", `{"body":`, 400},
	} {
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, remote.URL+sessionapi.ResourcePath+tc.kind, strings.NewReader(tc.body))
		request.Header.Set("Authorization", "Bearer "+readToken(t, tokenFile))
		request.Header.Set(sessionapi.ProtocolHeader, sessionapi.ProtocolVersion)
		response, err := remote.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != tc.status {
			t.Fatalf("resource status %d, want %d: %s, %v", response.StatusCode, tc.status, data, err)
		}
	}
}
