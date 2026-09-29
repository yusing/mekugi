package appserver

import (
	"bufio"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Wire subset from codex-rs/app-server-protocol/src/protocol/{common,v2}.rs
// at 86be5320; exercised against codex-cli 0.157.1. initialize is not version
// negotiation. Unknown notifications are ignored, not inferred from their text.
type Message struct {
	ID     jsontext.Value `json:"id,omitempty"`
	Method string         `json:"method,omitempty"`
	Params jsontext.Value `json:"params,omitzero"`
	Result jsontext.Value `json:"result,omitempty"`
	Error  *Error         `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Client struct {
	cmd         *exec.Cmd
	Input       io.WriteCloser
	Output      io.ReadCloser
	Messages    chan Message
	readDone    chan error
	stopRead    chan struct{}
	stopOnce    sync.Once
	Done        chan error
	Diagnostics Diagnostics
	next        int
}

// Diagnostics never share the RPC stream or write through the terminal painter.
type Diagnostics struct {
	sync.Mutex
	Text []byte
}

func (d *Diagnostics) Write(p []byte) (int, error) {
	d.Lock()
	defer d.Unlock()
	d.Text = append(d.Text, p...)
	if len(d.Text) > 64<<10 {
		d.Text = append([]byte(nil), d.Text[len(d.Text)-(64<<10):]...)
	}
	return len(p), nil
}

func Start(cmd *exec.Cmd) (*Client, error) {
	c := &Client{cmd: cmd, Messages: make(chan Message, 256), readDone: make(chan error, 1), stopRead: make(chan struct{}), Done: make(chan error, 1)}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, &c.Diagnostics
	var err error
	if c.Input, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if c.Output, err = cmd.StdoutPipe(); err != nil {
		c.Input.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		c.Input.Close()
		c.Output.Close()
		return nil, err
	}
	go func() {
		defer close(c.Messages)
		scanner := bufio.NewScanner(c.Output)
		scanner.Buffer(make([]byte, 64<<10), 16<<20)
		for scanner.Scan() {
			var message Message
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				c.readDone <- fmt.Errorf("decode app-server: %w", err)
				return
			}
			// Preserve ordered RPC events when presentation falls behind. The
			// bounded channel and stdout pipe backpressure the producer instead
			// of treating a temporary UI stall as a fatal protocol error.
			select {
			case c.Messages <- message:
			case <-c.stopRead:
				c.readDone <- nil
				return
			}
		}
		c.readDone <- scanner.Err()
	}()
	// Wait only after stdout reaches EOF, as required by StdoutPipe.
	go func() {
		readErr := <-c.readDone
		if readErr != nil {
			_ = cmd.Process.Kill()
		}
		c.Done <- errors.Join(readErr, cmd.Wait())
	}()
	return c, nil
}

func (c *Client) Send(method string, params any, request bool) (string, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	m := Message{Method: method, Params: data}
	if request {
		c.next++
		m.ID = jsontext.Value(fmt.Sprint(c.next))
	}
	encoded, err := json.Marshal(&m)
	if err != nil {
		return "", err
	}
	_, err = c.Input.Write(append(encoded, '\n'))
	return string(m.ID), err
}

// Respond answers a server-originated request using its original JSON-RPC ID.
func (c *Client) Respond(id jsontext.Value, result any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(&Message{ID: id, Result: data})
	if err != nil {
		return err
	}
	_, err = c.Input.Write(append(encoded, '\n'))
	return err
}

func (c *Client) Close() {
	// A paused consumer may leave the reader waiting to deliver a message,
	// rather than reading stdout. Closing the pipe alone cannot release it.
	c.stopOnce.Do(func() { close(c.stopRead) })
	_ = c.Input.Close()
	_ = c.cmd.Process.Kill()
	_ = c.Output.Close()
}

// EOF is Codex's graceful shutdown signal. Allow its thread/background-task
// owners to drain before falling back to forced cleanup.
func (c *Client) Shutdown() error {
	_ = c.Input.Close()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case err := <-c.Done:
		return err
	case <-timer.C:
		c.Close()
		return errors.Join(errors.New("app-server did not shut down after EOF; forced termination"), <-c.Done)
	}
}

func (c *Client) Initialize() (string, error) {
	return c.Send("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "mekugi", "version": "app-server-preview"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, true)
}

func Input(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text, "textElements": []any{}}}
}
