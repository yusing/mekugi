package router

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
type appServerMessage struct {
	ID     jsontext.Value  `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params jsontext.Value  `json:"params,omitempty"`
	Result jsontext.Value  `json:"result,omitempty"`
	Error  *appServerError `json:"error,omitempty"`
}

type appServerError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type appServerClient struct {
	cmd         *exec.Cmd
	input       io.WriteCloser
	output      io.ReadCloser
	messages    chan appServerMessage
	readDone    chan error
	done        chan error
	diagnostics appServerDiagnostics
	next        int
}

// Diagnostics never share the RPC stream or write through the terminal painter.
type appServerDiagnostics struct {
	sync.Mutex
	text []byte
}

func (d *appServerDiagnostics) Write(p []byte) (int, error) {
	d.Lock()
	defer d.Unlock()
	d.text = append(d.text, p...)
	if len(d.text) > 64<<10 {
		d.text = append([]byte(nil), d.text[len(d.text)-(64<<10):]...)
	}
	return len(p), nil
}

func startAppServer(cmd *exec.Cmd) (*appServerClient, error) {
	c := &appServerClient{cmd: cmd, messages: make(chan appServerMessage, 256), readDone: make(chan error, 1), done: make(chan error, 1)}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, &c.diagnostics
	var err error
	if c.input, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if c.output, err = cmd.StdoutPipe(); err != nil {
		c.input.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		c.input.Close()
		c.output.Close()
		return nil, err
	}
	go func() {
		defer close(c.messages)
		scanner := bufio.NewScanner(c.output)
		scanner.Buffer(make([]byte, 64<<10), 16<<20)
		for scanner.Scan() {
			var message appServerMessage
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				c.readDone <- fmt.Errorf("decode app-server: %w", err)
				return
			}
			select {
			case c.messages <- message:
			default:
				c.readDone <- errors.New("app-server event capacity exceeded; session state is incomplete, do not resubmit automatically")
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
		c.done <- errors.Join(readErr, cmd.Wait())
	}()
	return c, nil
}

func (c *appServerClient) send(method string, params any, request bool) (string, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	m := appServerMessage{Method: method, Params: data}
	if request {
		c.next++
		m.ID = jsontext.Value(fmt.Sprint(c.next))
	}
	encoded, err := json.Marshal(&m)
	if err != nil {
		return "", err
	}
	_, err = c.input.Write(append(encoded, '\n'))
	return string(m.ID), err
}

func (c *appServerClient) close() {
	_ = c.input.Close()
	_ = c.cmd.Process.Kill()
	_ = c.output.Close()
}

// EOF is Codex's graceful shutdown signal. Allow its thread/background-task
// owners to drain before falling back to forced cleanup.
func (c *appServerClient) shutdown() error {
	_ = c.input.Close()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case err := <-c.done:
		return err
	case <-timer.C:
		c.close()
		return errors.Join(errors.New("app-server did not shut down after EOF; forced termination"), <-c.done)
	}
}

func (c *appServerClient) initialize() (string, error) {
	return c.send("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "mekugi", "version": "app-server-preview"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, true)
}

func appServerInput(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text, "textElements": []any{}}}
}
