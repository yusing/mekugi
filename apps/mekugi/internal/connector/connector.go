// Package connector admits remote human requests to the private Session API.
package connector

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/libraries/persistence"
)

const (
	initializeLimit      = 1024
	initializeTimeout    = 5 * time.Second
	writeTimeout         = 10 * time.Second
	connectionLimit      = 128
	unauthenticatedLimit = 16
)

type Options struct {
	SocketPath string
	TokenFile  string
	Origins    []string
}

// Connector owns authentication and its upstream connections, never sessions.
type Connector struct {
	mu              sync.Mutex
	token           string
	tokenFile       string
	tokenLock       *os.File
	generation      context.Context
	cancel          context.CancelFunc
	closed          bool
	origins         map[string]bool
	transport       *http.Transport
	connections     int
	unauthenticated int
	relays          sync.WaitGroup
	closeDone       chan struct{}
	closeErr        error
}

func New(options Options) (*Connector, error) {
	if !filepath.IsAbs(options.SocketPath) || !filepath.IsAbs(options.TokenFile) {
		return nil, errors.New("connector socket and token file paths must be absolute")
	}
	c := &Connector{tokenFile: options.TokenFile, origins: make(map[string]bool), closeDone: make(chan struct{})}
	for _, origin := range options.Origins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || origin != u.Scheme+"://"+u.Host {
			return nil, errors.New("connector origins must be HTTP or HTTPS origins without paths")
		}
		c.origins[origin] = true
	}
	if err := c.loadToken(); err != nil {
		return nil, errors.Join(err, c.releaseTokenLock())
	}
	c.generation, c.cancel = context.WithCancel(context.Background())
	c.transport = sessionapi.ControlTransport(options.SocketPath)
	return c, nil
}

func (c *Connector) loadToken() error {
	directory := filepath.Dir(c.tokenFile)
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		err = persistence.EnsurePrivateDirectory(directory)
		if err == nil {
			info, err = os.Lstat(directory)
		}
	}
	if err != nil || !privateOwned(info, true) {
		return errors.New("connector token directory must be private and owned by the current user")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return errors.New("connector token directory must not contain symlinks")
	}
	// The stable lock survives token replacement and is never unlinked. Hold it
	// before reading or publishing credentials and until this connector closes.
	lock, err := os.OpenFile(c.tokenFile+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return errors.New("cannot open connector token lock")
	}
	info, err = lock.Stat()
	if err != nil || !privateOwned(info, false) || info.Size() != 0 {
		lock.Close()
		return errors.New("connector token lock must be an empty private regular file owned by the current user")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return errors.New("another connector already owns this token file")
		}
		return errors.New("cannot lock connector token file")
	}
	c.tokenLock = lock
	f, err := persistence.OpenRetained(c.tokenFile, 65)
	if errors.Is(err, os.ErrNotExist) {
		return c.publishToken()
	}
	if err != nil {
		return errors.New("cannot open connector token file")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !privateOwned(info, false) {
		return errors.New("connector token file must be private and owned by the current user")
	}
	data, err := io.ReadAll(io.LimitReader(f, 66))
	token := strings.TrimSuffix(string(data), "\n")
	decoded, decodeErr := hex.DecodeString(token)
	if err != nil || decodeErr != nil || len(decoded) != 32 || len(token) != 64 {
		return errors.New("invalid connector token file")
	}
	c.token = token
	return nil
}

func privateOwned(info os.FileInfo, directory bool) bool {
	if info == nil || info.Mode().Perm()&0o077 != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func (c *Connector) publishToken() error {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("cannot generate connector token")
	}
	token := hex.EncodeToString(random[:])
	if err := persistence.AtomicFile(c.tokenFile, ".token-*", []byte(token+"\n"), nil); err != nil {
		return errors.New("cannot publish connector token")
	}
	c.token = token
	return nil
}

// Rotate publishes the replacement before admitting it and cancels old requests.
// Failed publication leaves the previous token and requests unchanged.
func (c *Connector) Rotate() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("connector is closed")
	}
	if err := c.publishToken(); err != nil {
		return err
	}
	c.cancel()
	c.generation, c.cancel = context.WithCancel(context.Background())
	return nil
}

func (c *Connector) releaseTokenLock() error {
	if c.tokenLock == nil {
		return nil
	}
	// Explicit unlock also releases ownership if a fork still has the open file
	// description before exec closes its inherited descriptor.
	err := errors.Join(syscall.Flock(int(c.tokenLock.Fd()), syscall.LOCK_UN), c.tokenLock.Close())
	c.tokenLock = nil
	if err != nil {
		return errors.New("cannot release connector token lock")
	}
	return nil
}

func (c *Connector) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.closeDone
		return c.closeErr
	}
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	// Registration and closed admission share the mutex, so no relay can be
	// added after this wait begins. Only our connections, never sessions, end.
	c.relays.Wait()
	c.transport.CloseIdleConnections()
	c.closeErr = c.releaseTokenLock()
	close(c.closeDone)
	return c.closeErr
}

func (c *Connector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Origin")
	if origins := r.Header.Values("Origin"); len(origins) != 0 {
		if len(origins) != 1 || !c.origins[origins[0]] {
			http.Error(w, "This UI origin is not allowed.", http.StatusForbidden)
			return
		}
	}
	// Browser WebSockets use no preflight, custom headers or URL credentials.
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || r.URL.User != nil {
		http.Error(w, "Only the exact control endpoint is supported.", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet || r.URL.Path != sessionapi.RPCPath {
		http.NotFound(w, r)
		return
	}
	var protocols []string
	for _, line := range r.Header.Values("Sec-WebSocket-Protocol") {
		for protocol := range strings.SplitSeq(line, ",") {
			protocols = append(protocols, strings.TrimSpace(protocol))
		}
	}
	if !slices.Contains(protocols, sessionapi.RPCSubprotocol) {
		http.Error(w, "The control WebSocket subprotocol is required.", http.StatusUpgradeRequired)
		return
	}
	c.mu.Lock()
	if c.closed || c.connections >= connectionLimit || c.unauthenticated >= unauthenticatedLimit {
		c.mu.Unlock()
		http.Error(w, "The connector cannot accept another connection now.", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(c.generation)
	c.connections++
	c.unauthenticated++
	c.relays.Add(1)
	c.mu.Unlock()
	authenticated := false
	defer func() {
		cancel()
		c.mu.Lock()
		c.connections--
		if !authenticated {
			c.unauthenticated--
		}
		c.mu.Unlock()
		c.relays.Done()
	}()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()

	// Exact origin admission above deliberately replaces the library's host
	// patterns, which also allow same-host origins without configuration.
	downstream, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{sessionapi.RPCSubprotocol}, InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer downstream.CloseNow()
	downstream.SetReadLimit(initializeLimit)
	initializeCtx, stopInitialize := context.WithTimeout(ctx, initializeTimeout)
	defer stopInitialize()
	typ, data, err := downstream.Read(initializeCtx)
	if err != nil || typ != websocket.MessageText {
		return
	}
	var request sessionapi.RPCMessage
	var initialize sessionapi.RPCInitialize
	if json.Unmarshal(data, &request, json.RejectUnknownMembers(true)) != nil || request.JSONRPC != "2.0" || request.Method != "initialize" ||
		len(request.ID) == 0 || (request.ID.Kind() != '"' && request.ID.Kind() != '0') ||
		(request.ID.Kind() == '0' && strings.ContainsAny(string(request.ID), ".eE")) ||
		len(request.Result) != 0 || request.Error != nil || request.Params.Kind() != '{' ||
		json.Unmarshal(request.Params, &initialize, json.RejectUnknownMembers(true)) != nil {
		return
	}
	c.mu.Lock()
	valid := ctx.Err() == nil && subtle.ConstantTimeCompare([]byte(initialize.Token), []byte(c.token)) == 1
	if valid {
		authenticated = true
		c.unauthenticated--
	}
	c.mu.Unlock()
	if !valid {
		refuse(initializeCtx, downstream, request, http.StatusUnauthorized, "A valid connector token is required.")
		return
	}
	initialize.Token = ""
	request.Params, _ = json.Marshal(&initialize)
	data, _ = json.Marshal(&request)
	// Build the upstream handshake from owned facts only. Caller credentials,
	// Origin and authority headers never reach the private socket.
	upstream, response, err := websocket.Dial(initializeCtx, "ws://session"+sessionapi.RPCPath, &websocket.DialOptions{
		HTTPClient:   &http.Client{Transport: c.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		HTTPHeader:   http.Header{sessionapi.ProtocolHeader: []string{sessionapi.ProtocolVersion}},
		Subprotocols: []string{sessionapi.RPCSubprotocol},
	})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		refuse(initializeCtx, downstream, request, http.StatusBadGateway, "The session server cannot be reached.")
		return
	}
	defer upstream.CloseNow()
	if upstream.Subprotocol() != sessionapi.RPCSubprotocol {
		refuse(initializeCtx, downstream, request, http.StatusBadGateway, "The session server protocol is incompatible.")
		return
	}
	if err := upstream.Write(initializeCtx, websocket.MessageText, data); err != nil {
		return
	}
	stopInitialize()
	downstream.SetReadLimit(sessionapi.RPCMessageLimit)
	upstream.SetReadLimit(sessionapi.RPCPublicationLimit)
	// Each direction has one reader and writer. Cancellation releases blocked
	// reads/writes, and both loops are joined before this connection retires.
	done := make(chan struct{}, 2)
	for _, pair := range [][2]*websocket.Conn{{downstream, upstream}, {upstream, downstream}} {
		go func() {
			defer func() { cancel(); done <- struct{}{} }()
			relay(ctx, pair[0], pair[1])
		}()
	}
	<-done
	<-done
}

func refuse(ctx context.Context, conn *websocket.Conn, request sessionapi.RPCMessage, status int, message string) {
	// Cancellation retires the transport, rather than reporting a new admission
	// failure. An immediately writable WebSocket can outpace context cancellation.
	if ctx.Err() != nil {
		return
	}
	response := sessionapi.RPCMessage{JSONRPC: "2.0", ID: request.ID, Error: &sessionapi.RPCError{
		Code: -32000, Message: message, Data: &sessionapi.RPCErrorData{Status: status, Outcome: "not_admitted"},
	}}
	data, _ := json.Marshal(&response)
	_ = conn.Write(ctx, websocket.MessageText, data)
}

func relay(ctx context.Context, source, destination *websocket.Conn) {
	for {
		typ, reader, err := source.Reader(ctx)
		if err != nil || typ != websocket.MessageText {
			return
		}
		if sessionapi.WriteMessage(ctx, destination, typ, reader, writeTimeout) != nil {
			return
		}
	}
}
