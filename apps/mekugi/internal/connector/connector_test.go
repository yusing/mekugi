package connector

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func upstream(t *testing.T, handler http.Handler) (string, *httptest.Server) {
	t.Helper()
	directory, err := os.MkdirTemp("", "mekugi-connector-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return socket, server
}

func connect(t *testing.T, socket string, origins ...string) (*Connector, string) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "private", "token")
	c, err := New(Options{SocketPath: socket, TokenFile: path, Origins: origins})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c, path
}

func readToken(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func dial(t *testing.T, base string, headers http.Header) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, base+sessionapi.RPCPath, &websocket.DialOptions{
		Subprotocols: []string{sessionapi.RPCSubprotocol}, HTTPHeader: headers,
		CompressionMode: websocket.CompressionNoContextTakeover,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func initialize(t *testing.T, conn *websocket.Conn, token string) sessionapi.RPCMessage {
	t.Helper()
	params, err := json.Marshal(&sessionapi.RPCInitialize{Version: sessionapi.ProtocolVersion, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	message, err := json.Marshal(&sessionapi.RPCMessage{JSONRPC: "2.0", ID: []byte(`"init"`), Method: "initialize", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, message); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reply sessionapi.RPCMessage
	if err := json.Unmarshal(data, &reply); err != nil || string(reply.ID) != `"init"` {
		t.Fatalf("initialization response = %s, %v", data, err)
	}
	return reply
}

func initializeFailure(t *testing.T, c *Connector, token string, status int) {
	t.Helper()
	remote := httptest.NewServer(c)
	defer remote.Close()
	conn := dial(t, remote.URL, nil)
	defer conn.CloseNow()
	reply := initialize(t, conn, token)
	if reply.Error == nil || reply.Error.Data == nil || reply.Error.Data.Status != status || reply.Error.Data.Outcome != "not_admitted" {
		t.Fatalf("initialization error = %+v, want status %d", reply.Error, status)
	}
}

// rpcUpstream validates the actual private handshake and stripped initialization
// before exposing the connection to the test's server operation.
func rpcUpstream(t *testing.T, operation func(*websocket.Conn)) string {
	t.Helper()
	socket, _ := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != sessionapi.RPCPath || r.Header.Get(sessionapi.ProtocolHeader) != sessionapi.ProtocolVersion {
			t.Errorf("incorrect upstream endpoint or version: %s %q", r.URL.RequestURI(), r.Header.Get(sessionapi.ProtocolHeader))
		}
		for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Origin", "X-Mekugi-Thread", "X-Mekugi-Authority", "Forwarded", "X-Forwarded-For", "Idempotency-Key", "Sec-WebSocket-Extensions"} {
			if r.Header.Get(key) != "" {
				t.Errorf("forwarded caller header %s", key)
			}
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{sessionapi.RPCSubprotocol}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(sessionapi.RPCMessageLimit)
		_, data, err := conn.Read(t.Context())
		var init sessionapi.RPCMessage
		var params sessionapi.RPCInitialize
		if err != nil || json.Unmarshal(data, &init) != nil || json.Unmarshal(init.Params, &params) != nil || init.Method != "initialize" || params.Token != "" {
			t.Error("private initialization was not sanitized")
			return
		}
		result, _ := json.Marshal(&sessionapi.RPCInitialize{Version: sessionapi.ProtocolVersion})
		message := sessionapi.RPCMessage{JSONRPC: "2.0", ID: init.ID, Result: result}
		if params.Version != sessionapi.ProtocolVersion {
			message.Result = nil
			message.Error = &sessionapi.RPCError{Code: -32000, Message: "incompatible version", Data: &sessionapi.RPCErrorData{Status: 409, Outcome: "not_admitted"}}
		}
		data, _ = json.Marshal(&message)
		if err := conn.Write(t.Context(), websocket.MessageText, data); err != nil {
			return
		}
		if message.Error == nil {
			operation(conn)
		}
	}))
	return socket
}

func echo(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	for {
		typ, data, err := conn.Read(t.Context())
		if err != nil {
			return
		}
		if err := conn.Write(t.Context(), typ, data); err != nil {
			return
		}
	}
}

func TestConnectorCompressionNegotiation(t *testing.T) {
	for _, mode := range []websocket.CompressionMode{websocket.CompressionDisabled, websocket.CompressionNoContextTakeover} {
		t.Run(map[websocket.CompressionMode]string{websocket.CompressionDisabled: "uncompressed", websocket.CompressionNoContextTakeover: "no-context-takeover"}[mode], func(t *testing.T) {
			socket := rpcUpstream(t, func(conn *websocket.Conn) { echo(t, conn) })
			bridge, path := connect(t, socket)
			server := httptest.NewServer(bridge)
			defer server.Close()
			conn, response, err := websocket.Dial(t.Context(), server.URL+sessionapi.RPCPath, &websocket.DialOptions{Subprotocols: []string{sessionapi.RPCSubprotocol}, CompressionMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			extension := response.Header.Get("Sec-WebSocket-Extensions")
			if mode == websocket.CompressionDisabled && extension != "" {
				t.Fatalf("unrequested compression: %s", extension)
			}
			if mode != websocket.CompressionDisabled && (!strings.Contains(extension, "client_no_context_takeover") || !strings.Contains(extension, "server_no_context_takeover")) {
				t.Fatalf("compression retained context: %s", extension)
			}
			if reply := initialize(t, conn, readToken(t, path)); reply.Error != nil {
				t.Fatal(reply.Error)
			}
			conn.SetReadLimit(sessionapi.RPCPublicationLimit)
			payload := []byte(strings.Repeat(`{"entry":"retained authored content","revision":1}`+"\n", 2000))
			for range 2 {
				if err := conn.Write(t.Context(), websocket.MessageText, payload); err != nil {
					t.Fatal(err)
				}
				_, received, err := conn.Read(t.Context())
				if err != nil || string(received) != string(payload) {
					t.Fatalf("remote encoding changed forwarded bytes: %v", err)
				}
			}
		})
	}
}

func exchange(t *testing.T, conn *websocket.Conn, message string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
		t.Fatal(err)
	}
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText {
		t.Fatalf("read message: %v, type %v", err, typ)
	}
	return string(data)
}

func TestConnectorTokenPersistenceAndPrivateFiles(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(t.TempDir(), "s")
	c, path := connect(t, socket)
	token := readToken(t, path)
	if len(token) != 64 {
		t.Fatal("wrong token length")
	}
	for path, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("private permissions: %v %v", info, err)
		}
	}
	c.Close()
	reopened, err := New(Options{SocketPath: socket, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if readToken(t, path) != token {
		t.Fatal("restart replaced persistent token")
	}
	initializeFailure(t, reopened, token, http.StatusBadGateway)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{SocketPath: socket, TokenFile: path}); err == nil {
		t.Fatal("accepted exposed token file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{SocketPath: socket, TokenFile: path}); err == nil {
		t.Fatal("accepted exposed token directory")
	}
}

func TestConnectorTokenLifetimeOwnership(t *testing.T) {
	t.Parallel()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options := Options{SocketPath: filepath.Join(directory, "s"), TokenFile: filepath.Join(directory, "private", "token")}
	type result struct {
		connector *Connector
		err       error
	}
	start, results := make(chan struct{}), make(chan result, 12)
	for range cap(results) {
		go func() {
			<-start
			c, err := New(options)
			results <- result{c, err}
		}()
	}
	close(start)
	var owner *Connector
	owners := 0
	for range cap(results) {
		result := <-results
		if result.err == nil {
			owners++
			owner = result.connector
			t.Cleanup(func() { _ = result.connector.Close() })
		} else if result.err.Error() != "another connector already owns this token file" {
			t.Errorf("concurrent initialization failed unexpectedly: %v", result.err)
		}
	}
	if owners != 1 {
		t.Fatalf("concurrent owners = %d", owners)
	}
	token := readToken(t, options.TokenFile)
	initializeFailure(t, owner, token, http.StatusBadGateway)
	if second, err := New(options); err == nil {
		second.Close()
		t.Fatal("second owner admitted for an existing token")
	}
	other, err := New(Options{SocketPath: options.SocketPath, TokenFile: options.TokenFile + ".other"})
	if err != nil {
		t.Fatal("independent token file refused:", err)
	}
	defer other.Close()
	lockBefore, err := os.Stat(options.TokenFile + ".lock")
	if err != nil || lockBefore.Mode().Perm() != 0o600 {
		t.Fatalf("private token lock: %v %v", lockBefore, err)
	}
	// A duplicated open description survives Close, as it can during fork/exec.
	// Reacquisition proves Close explicitly unlocks rather than relying on close.
	duplicate, err := syscall.Dup(int(owner.tokenLock.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(duplicate)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := New(options)
	if err != nil {
		t.Fatal("token ownership was not released:", err)
	}
	defer resumed.Close()
	lockAfter, err := os.Stat(options.TokenFile + ".lock")
	if err != nil || !os.SameFile(lockBefore, lockAfter) || readToken(t, options.TokenFile) != token {
		t.Fatal("reopening replaced the stable lock or persistent token")
	}
	if err := resumed.Rotate(); err != nil {
		t.Fatal(err)
	}
	if readToken(t, options.TokenFile) == token {
		t.Fatal("new owner could not rotate the token")
	}
	initializeFailure(t, other, readToken(t, options.TokenFile+".other"), http.StatusBadGateway)
}

func TestConnectorTokenLockRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"symlink", "public", "nonempty", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "token")
			switch kind {
			case "symlink":
				err = os.Symlink(filepath.Join(directory, "missing"), path+".lock")
			case "public":
				err = os.WriteFile(path+".lock", nil, 0o644)
			case "nonempty":
				err = os.WriteFile(path+".lock", []byte("unrelated file"), 0o600)
			case "fifo":
				err = syscall.Mkfifo(path+".lock", 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if c, err := New(Options{SocketPath: filepath.Join(directory, "s"), TokenFile: path}); err == nil {
				c.Close()
				t.Fatal("accepted unsafe token lock")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("published token before lock admission: %v", err)
			}
			// A refused lock remains untouched and can be explicitly repaired.
			if err := os.Remove(path + ".lock"); err != nil {
				t.Fatal(err)
			}
			c, err := New(Options{SocketPath: filepath.Join(directory, "s"), TokenFile: path})
			if err != nil {
				t.Fatal("repaired token lock remained unavailable:", err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConnectorTokenRejectsSymlinksAndInvalidContents(t *testing.T) {
	t.Parallel()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "token")
	secret := "sensitive-invalid-token"
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	options := Options{SocketPath: filepath.Join(directory, "s"), TokenFile: path}
	if _, err := New(options); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid token error = %v", err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	options.TokenFile = link
	if _, err := New(options); err == nil {
		t.Fatal("accepted symlink token")
	}
	link = filepath.Join(directory, "linked-directory")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	options.TokenFile = filepath.Join(link, "missing-token")
	if _, err := New(options); err == nil {
		t.Fatal("accepted symlink directory")
	}
}

func TestConnectorForwardsExactMessagesWithoutAuthority(t *testing.T) {
	t.Parallel()
	socket := rpcUpstream(t, func(conn *websocket.Conn) { echo(t, conn) })
	c, path := connect(t, socket, "https://ui.example")
	remote := httptest.NewServer(c)
	defer remote.Close()
	headers := http.Header{"Origin": []string{"https://ui.example"}}
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Mekugi-Thread", "X-Mekugi-Authority", "Forwarded", "X-Forwarded-For", "Idempotency-Key", sessionapi.ProtocolHeader} {
		headers.Set(key, "caller-claim")
	}
	conn := dial(t, remote.URL, headers)
	defer conn.CloseNow()
	if reply := initialize(t, conn, readToken(t, path)); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	const message = ` {"jsonrpc":"2.0", "id":123,"method":"session/input","params":{"thread":"123","body":{"text":"hello\n世界", "unknown":[2,1]}}} ` + "\n"
	if got := exchange(t, conn, message); got != message {
		t.Fatalf("changed message: %q", got)
	}
}

func TestConnectorOriginAndEndpointAdmission(t *testing.T) {
	t.Parallel()
	c, _ := connect(t, filepath.Join(t.TempDir(), "absent"), "https://ui.example")
	for _, tc := range []struct {
		name, method, path, protocol string
		origins                      []string
		want                         int
	}{
		{"other origin", "GET", sessionapi.RPCPath, sessionapi.RPCSubprotocol, []string{"https://other.example"}, 403},
		{"null origin", "GET", sessionapi.RPCPath, sessionapi.RPCSubprotocol, []string{"null"}, 403},
		{"empty origin", "GET", sessionapi.RPCPath, sessionapi.RPCSubprotocol, []string{""}, 403},
		{"multiple origins", "GET", sessionapi.RPCPath, sessionapi.RPCSubprotocol, []string{"https://ui.example", "https://ui.example"}, 403},
		{"preflight", "OPTIONS", sessionapi.RPCPath, sessionapi.RPCSubprotocol, []string{"https://ui.example"}, 404},
		{"query token", "GET", sessionapi.RPCPath + "?token=credential", sessionapi.RPCSubprotocol, nil, 400},
		{"empty query", "GET", sessionapi.RPCPath + "?", sessionapi.RPCSubprotocol, nil, 400},
		{"encoded path", "GET", "/api/%63ontrol", sessionapi.RPCSubprotocol, nil, 400},
		{"native endpoint", "GET", sessionapi.NativeRPCPath, sessionapi.RPCSubprotocol, nil, 404},
		{"wrong method", "POST", sessionapi.RPCPath, sessionapi.RPCSubprotocol, nil, 404},
		{"missing protocol", "GET", sessionapi.RPCPath, "", nil, 426},
		{"incompatible protocol", "GET", sessionapi.RPCPath, "mekugi.control.v1", nil, 426},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header["Origin"] = tc.origins
			r.Header.Set("Sec-WebSocket-Protocol", tc.protocol)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestConnectorAuthenticationBeforeUpstream(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	socket, _ := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	c, path := connect(t, socket, "https://ui.example")
	remote := httptest.NewServer(c)
	defer remote.Close()
	for _, token := range []string{"", "wrong"} {
		conn := dial(t, remote.URL, http.Header{"Origin": []string{"https://ui.example"}, "Cookie": []string{"token=" + readToken(t, path)}, "Authorization": []string{"Bearer " + readToken(t, path)}})
		if reply := initialize(t, conn, token); reply.Error == nil || reply.Error.Data.Status != 401 {
			t.Fatalf("token %q was not refused", token)
		}
		conn.CloseNow()
	}
	for _, message := range []string{
		strings.Repeat("x", initializeLimit+1),
		`[{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}]`,
		`{"jsonrpc":"2.0","method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"token":"wrong","token":"duplicate"}}`,
	} {
		conn := dial(t, remote.URL, nil)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_ = conn.Write(ctx, websocket.MessageText, []byte(message))
		if _, _, err := conn.Read(ctx); err == nil {
			t.Error("invalid initialization received server data")
		}
		cancel()
		conn.CloseNow()
	}
	if calls.Load() != 0 {
		t.Fatalf("unauthenticated upstream requests = %d", calls.Load())
	}
}

func TestConnectorUsesServerVersionAdmission(t *testing.T) {
	t.Parallel()
	hub := sessionapi.NewHub(t.Context(), nil)
	socket, _ := upstream(t, sessionapi.Versioned(hub.Handler()))
	c, path := connect(t, socket)
	remote := httptest.NewServer(c)
	defer remote.Close()
	for _, version := range []string{"", "incompatible"} {
		conn := dial(t, remote.URL, nil)
		params, _ := json.Marshal(&sessionapi.RPCInitialize{Version: version, Token: readToken(t, path)})
		message, _ := json.Marshal(&sessionapi.RPCMessage{JSONRPC: "2.0", ID: []byte("91"), Method: "initialize", Params: params})
		var reply sessionapi.RPCMessage
		if err := json.Unmarshal([]byte(exchange(t, conn, string(message))), &reply); err != nil || reply.Error == nil || reply.Error.Data.Status != 409 || string(reply.ID) != "91" {
			t.Fatalf("version %q: response %+v, %v", version, reply, err)
		}
		conn.CloseNow()
	}
}

func TestConnectorRotationAndIndependentLifetimes(t *testing.T) {
	t.Parallel()
	socket := rpcUpstream(t, func(conn *websocket.Conn) { echo(t, conn) })
	one, path := connect(t, socket)
	two, pathTwo := connect(t, socket)
	oneHTTP, twoHTTP := httptest.NewServer(one), httptest.NewServer(two)
	defer oneHTTP.Close()
	defer twoHTTP.Close()
	old := readToken(t, path)
	conn := dial(t, oneHTTP.URL, nil)
	if reply := initialize(t, conn, old); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	other := dial(t, twoHTTP.URL, nil)
	defer other.CloseNow()
	if reply := initialize(t, other, readToken(t, pathTwo)); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	// Fail actual publication without depending on chmod behavior under root.
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := one.Rotate(); err == nil || strings.Contains(err.Error(), old) {
		t.Fatalf("failed rotation = %v", err)
	}
	const message = `{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`
	if got := exchange(t, conn, message); got != message {
		t.Fatal("failed rotation changed connection")
	}
	freshOld := dial(t, oneHTTP.URL, nil)
	if reply := initialize(t, freshOld, old); reply.Error != nil {
		t.Fatal("failed rotation revoked token")
	}
	freshOld.CloseNow()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if err := one.Rotate(); err != nil {
		t.Fatal(err)
	}
	if readToken(t, path) == old {
		t.Fatal("rotation retained token")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("rotation left old connection open")
	}
	initializeFailure(t, one, old, 401)
	replacement := dial(t, oneHTTP.URL, nil)
	if reply := initialize(t, replacement, readToken(t, path)); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	if got := exchange(t, other, message); got != message {
		t.Fatal("connector shutdown affected independent connection")
	}
	// A new connection through the other connector still reaches the server.
	fresh := dial(t, twoHTTP.URL, nil)
	defer fresh.CloseNow()
	if reply := initialize(t, fresh, readToken(t, pathTwo)); reply.Error != nil {
		t.Fatal(reply.Error)
	}
}

func TestConnectorMutationFailureDoesNotRetry(t *testing.T) {
	t.Parallel()
	var mutations atomic.Int32
	socket := rpcUpstream(t, func(conn *websocket.Conn) {
		if _, _, err := conn.Read(t.Context()); err == nil {
			mutations.Add(1)
		}
	})
	c, path := connect(t, socket)
	remote := httptest.NewServer(c)
	defer remote.Close()
	conn := dial(t, remote.URL, nil)
	if reply := initialize(t, conn, readToken(t, path)); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":2,"method":"session/input","params":{"thread":"123","body":{"text":"once"}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("mutation transport loss was hidden")
	}
	if mutations.Load() != 1 {
		t.Fatalf("mutation attempts = %d", mutations.Load())
	}
}

func TestConnectorLargeMessageAndClientDisconnect(t *testing.T) {
	t.Parallel()
	received, canceled := make(chan int64, 1), make(chan struct{})
	socket := rpcUpstream(t, func(conn *websocket.Conn) {
		_, reader, err := conn.Reader(t.Context())
		if err != nil {
			t.Error(err)
			return
		}
		n, err := io.Copy(io.Discard, reader)
		if err != nil {
			t.Error(err)
			return
		}
		received <- n
		_, _, _ = conn.Read(t.Context())
		close(canceled)
	})
	c, path := connect(t, socket)
	remote := httptest.NewServer(c)
	defer remote.Close()
	conn := dial(t, remote.URL, nil)
	if reply := initialize(t, conn, readToken(t, path)); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	message := []byte(`{"jsonrpc":"2.0","id":2,"method":"session/input","params":{"thread":"one","body":{"text":"` + strings.Repeat("x", sessionapi.InputBodyLimit-200) + `"}}}`)
	if err := conn.Write(ctx, websocket.MessageText, message); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-received:
		if n != int64(len(message)) {
			t.Fatalf("received bytes = %d, want %d", n, len(message))
		}
	case <-ctx.Done():
		t.Fatal("large message did not arrive")
	}
	conn.CloseNow()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("client disconnect did not close upstream")
	}
}
