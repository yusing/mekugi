// Package claude owns the optional Claude Agent SDK subprocess transport.
package claude

import (
	"bufio"
	"context"
	"embed"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed all:dist
var bundle embed.FS

// Config captures the admitted runtime. Executable paths must be absolute;
// Env is the complete child environment, not an overlay on ambient authority.
type Config struct {
	BunPath, ClaudePath, Workspace string
	Env                            []string
	Options                        jsontext.Value
	Tools                          []Tool
	OnRequest                      func(context.Context, string, jsontext.Value) (jsontext.Value, error)
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema jsontext.Value `json:"inputSchema"`
}

// Input is an attributed native SDK input. A queued acknowledgement is local;
// native command_lifecycle.started confirms dispatch and result input UUIDs
// confirm consumption. Synthetic inputs need not appear in replay or history.
type Input struct {
	Type            string         `json:"type"`
	UUID            string         `json:"uuid"`
	SessionID       string         `json:"session_id,omitempty"`
	Message         jsontext.Value `json:"message"`
	ParentToolUseID *string        `json:"parent_tool_use_id"`
	IsSynthetic     bool           `json:"isSynthetic,omitzero"`
	Origin          jsontext.Value `json:"origin,omitempty"`
	ShouldQuery     *bool          `json:"shouldQuery,omitzero"`
	Priority        string         `json:"priority,omitempty"`
}

// Event exposes identity/lifecycle fields while leaving SDK payloads intact.
type Event struct {
	Type             string         `json:"type"`
	Subtype          string         `json:"subtype"`
	UUID             string         `json:"uuid"`
	SessionID        string         `json:"session_id"`
	TerminalReason   string         `json:"terminal_reason"`
	CommandUUID      string         `json:"command_uuid"`
	State            string         `json:"state"`
	UserMessageUUID  string         `json:"user_message_uuid"`
	UserMessageUUIDs []string       `json:"user_message_uuids"`
	NumTurns         *int           `json:"num_turns"`
	Message          jsontext.Value `json:"message"`
}

type frame struct {
	Kind   string         `json:"kind"`
	ID     string         `json:"id,omitempty"`
	Method string         `json:"method,omitempty"`
	Params jsontext.Value `json:"params,omitempty"`
	Result jsontext.Value `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
	Event  jsontext.Value `json:"event,omitempty"`
}

type outbound struct {
	data []byte
	sent chan error
}

type Client struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	cancel         context.CancelFunc
	ctx            context.Context
	done           chan struct{}
	events         chan jsontext.Value
	outbound       chan outbound
	writerDone     chan struct{}
	mu             sync.Mutex
	pending        map[string]chan frame
	callbacks      map[string]context.CancelFunc
	onRequest      func(context.Context, string, jsontext.Value) (jsontext.Value, error)
	callbackWG     sync.WaitGroup
	sequence       atomic.Uint64
	closeOnce      sync.Once
	closing        atomic.Bool
	err            error
	initialization jsontext.Value
	diagnostics    diagnosticTail
}

// Start returns only after the SDK's initialization handshake succeeds. The
// caller must consume Events until it closes and keep ctx alive for the runtime.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	worker, err := bundle.ReadFile("dist/worker.mjs")
	if err != nil {
		return nil, errors.New("Claude worker is not bundled; run make claude-worker, then rebuild mekugi")
	}
	return start(ctx, cfg, worker)
}

func start(ctx context.Context, cfg Config, worker []byte) (*Client, error) {
	if !filepath.IsAbs(cfg.BunPath) || !filepath.IsAbs(cfg.ClaudePath) || !filepath.IsAbs(cfg.Workspace) {
		return nil, errors.New("Claude runtime requires absolute Bun, Claude and workspace paths")
	}
	if cfg.Env == nil {
		return nil, errors.New("Claude runtime requires an explicitly captured environment")
	}
	if cfg.OnRequest == nil {
		return nil, errors.New("Claude runtime requires a host hook and permission handler")
	}
	dir, err := os.MkdirTemp("", "mekugi-claude-worker-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "worker.mjs")
	if err := os.WriteFile(path, worker, 0600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	c := &Client{ctx: childCtx, cancel: cancel, done: make(chan struct{}),
		outbound: make(chan outbound), writerDone: make(chan struct{}),
		events: make(chan jsontext.Value, 128), pending: make(map[string]chan frame),
		callbacks: make(map[string]context.CancelFunc), onRequest: cfg.OnRequest}
	c.cmd = exec.CommandContext(childCtx, cfg.BunPath, path)
	c.cmd.Dir, c.cmd.Env, c.cmd.Stderr = cfg.Workspace, cfg.Env, &c.diagnostics
	c.cmd.WaitDelay = 5 * time.Second
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	// Closing the host pipe lets the worker close the SDK and its native child.
	// CommandContext's WaitDelay still kills a worker that cannot shut down.
	c.cmd.Cancel = func() error { _ = c.stdin.Close(); return nil }
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = c.stdin.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := c.cmd.Start(); err != nil {
		cancel()
		_ = c.stdin.Close()
		_ = stdout.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start Claude SDK worker: %w", err)
	}
	go c.writeLoop()
	go func() {
		readErr := c.read(stdout)
		if readErr != nil {
			c.mu.Lock()
			c.err = readErr
			c.mu.Unlock()
			cancel()
		}
		waitErr := c.cmd.Wait()
		if readErr == nil && waitErr == nil && !c.closing.Load() {
			readErr = errors.New("Claude worker transport closed unexpectedly; input acceptance may be uncertain")
		}
		c.mu.Lock()
		c.err = errors.Join(readErr, waitErr)
		for _, stop := range c.callbacks {
			stop()
		}
		c.mu.Unlock()
		cancel()
		c.callbackWG.Wait()
		<-c.writerDone
		_ = os.RemoveAll(dir)
		close(c.events)
		close(c.done)
	}()
	params, err := json.Marshal(&struct {
		ClaudePath string         `json:"claudePath"`
		Workspace  string         `json:"workspace"`
		Options    jsontext.Value `json:"options,omitempty"`
		Tools      []Tool         `json:"tools"`
	}{cfg.ClaudePath, cfg.Workspace, cfg.Options, cfg.Tools})
	if err == nil {
		c.initialization, err = c.Request(ctx, "initialize", params)
	}
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize Claude SDK: %w", err)
	}
	return c, nil
}

func (c *Client) Initialization() jsontext.Value {
	return append(jsontext.Value(nil), c.initialization...)
}
func (c *Client) Events() <-chan jsontext.Value { return c.events }

// Request cancellation stops waiting, not native execution. In particular an
// uncertain input must be reconciled rather than automatically submitted again.
func (c *Client) Request(ctx context.Context, method string, params jsontext.Value) (jsontext.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := strconv.FormatUint(c.sequence.Add(1), 10)
	response := make(chan frame, 1)
	c.mu.Lock()
	c.pending[id] = response
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.write(ctx, frame{Kind: "request", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case reply := <-response:
		if reply.Error != "" {
			return nil, errors.New(reply.Error)
		}
		return reply.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.transportError()
	}
}

func (c *Client) write(ctx context.Context, message frame) error {
	data, err := json.Marshal(&message)
	if err != nil {
		return err
	}
	send := outbound{data: append(data, '\n'), sent: make(chan error, 1)}
	select {
	case c.outbound <- send:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
	select {
	case err := <-send.sent:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

func (c *Client) writeLoop() {
	defer close(c.writerDone)
	for {
		select {
		case send := <-c.outbound:
			_, err := c.stdin.Write(send.data)
			send.sent <- err
			if err != nil {
				c.cancel()
				return
			}
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Client) read(stdout io.Reader) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var message frame
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("Claude worker protocol: %w", err)
		}
		switch message.Kind {
		case "response":
			c.mu.Lock()
			pending := c.pending[message.ID]
			c.mu.Unlock()
			if pending != nil {
				pending <- message
			}
		case "event":
			select {
			case c.events <- message.Event:
			case <-c.ctx.Done():
				return nil
			}
		case "request":
			ctx, cancel := context.WithCancel(c.ctx)
			c.mu.Lock()
			c.callbacks[message.ID] = cancel
			c.mu.Unlock()
			c.callbackWG.Add(1)
			go func() {
				defer c.callbackWG.Done()
				defer cancel()
				result, err := c.onRequest(ctx, message.Method, message.Params)
				reply := frame{Kind: "response", ID: message.ID, Result: result}
				if err != nil {
					reply.Error = err.Error()
					reply.Result = nil
				}
				_ = c.write(ctx, reply)
				c.mu.Lock()
				delete(c.callbacks, message.ID)
				c.mu.Unlock()
			}()
		case "cancel":
			c.mu.Lock()
			cancel := c.callbacks[message.ID]
			c.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		case "fatal":
			return fmt.Errorf("Claude SDK: %s", message.Error)
		default:
			return fmt.Errorf("Claude worker unknown frame kind %q", message.Kind)
		}
	}
	return scanner.Err()
}

func (c *Client) transportError() error {
	if err := c.Err(); err != nil {
		return err
	}
	return errors.New("Claude worker transport closed; input acceptance may be uncertain")
}

// Err reports the process outcome once its owned runtime has stopped.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Diagnostics returns the most recent 64 KiB of worker stderr, separately from
// the protocol. Callers decide whether and where private diagnostics are shown.
func (c *Client) Diagnostics() string { return c.diagnostics.String() }

// Close joins the worker and callback lifetimes, allowing graceful SDK shutdown
// before killing an unresponsive worker. Host callbacks must honor cancellation.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = c.Request(ctx, "shutdown", nil)
		_ = c.stdin.Close()
		select {
		case <-c.done:
		case <-ctx.Done():
			c.cancel()
			<-c.done
		}
	})
	return c.Err()
}

type diagnosticTail struct {
	mu   sync.Mutex
	data []byte
}

func (d *diagnosticTail) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	const limit = 64 * 1024
	n := len(p)
	if len(p) >= limit {
		d.data = append(d.data[:0], p[len(p)-limit:]...)
		return n, nil
	}
	if extra := len(d.data) + len(p) - limit; extra > 0 {
		copy(d.data, d.data[extra:])
		d.data = d.data[:len(d.data)-extra]
	}
	d.data = append(d.data, p...)
	return n, nil
}
func (d *diagnosticTail) String() string { d.mu.Lock(); defer d.mu.Unlock(); return string(d.data) }
