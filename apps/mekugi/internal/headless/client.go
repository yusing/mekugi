// Package headless owns the noninteractive app-server client and its JSONL evidence.
package headless

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/notifications/notice"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: apps/mekugi/internal/router/app_server_headless.go:18:205@b4e9e2fb

// Start reads one prompt and starts the host. The returned function owns JSONL
// delivery and joins host shutdown; callers must run it after successful startup.
func Start(ctx context.Context, cmd *exec.Cmd, input io.Reader, output io.Writer, notices *notice.Service) (func() error, error) {
	const maxPrompt = 16 << 20
	type promptResult struct {
		data []byte
		err  error
	}
	promptC := make(chan promptResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(input, maxPrompt+1))
		promptC <- promptResult{data, err}
	}()
	var prompt string
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-promptC:
		if result.err != nil {
			return nil, result.err
		}
		if len(result.data) > maxPrompt {
			return nil, errors.New("headless prompt exceeds 16 MiB")
		}
		prompt = string(result.data)
		if strings.TrimSpace(prompt) == "" {
			return nil, errors.New("headless requires a nonempty prompt on stdin")
		}
	}
	c, err := appserver.Start(cmd)
	if err != nil {
		return nil, err
	}
	requests := sessioncontrol.NewRequests[struct{}](c)
	if _, err := requests.Initialize(struct{}{}); err != nil {
		c.Close()
		<-c.Done
		return nil, hostError(c, err)
	}
	h := &client{ctx: ctx, host: c, requests: requests, notices: notices, output: jsontext.NewEncoder(output), prompt: prompt}
	return h.run, nil
}

type client struct {
	ctx       context.Context
	host      *appserver.Client
	requests  *sessioncontrol.Requests[struct{}]
	notices   *notice.Service
	output    *jsontext.Encoder
	prompt    string
	thread    string
	turn      string
	completed bool
}

func (h *client) emit(method string, params any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.MarshalEncode(h.output, &appserver.Message{Method: method, Params: data})
}

func (h *client) run() (runErr error) {
	exited := false
	defer func() {
		defer h.host.Input.Close()
		if !exited {
			runErr = errors.Join(runErr, h.host.Shutdown())
		}
		runErr = hostError(h.host, runErr)
	}()
	for {
		select {
		case <-h.ctx.Done():
			return h.ctx.Err()
		case err := <-h.host.Done:
			exited = true
			return errors.Join(errors.New("headless app-server disconnected before completion"), err)
		case m, ok := <-h.host.Messages:
			if !ok {
				return errors.New("headless app-server output ended before completion")
			}
			if err := json.MarshalEncode(h.output, &m); err != nil {
				return err
			}
			if err := h.message(m); err != nil {
				return err
			}
		}
		if h.completed && !h.requests.Pending() {
			return h.emit("mekugi/headless/completed", map[string]any{"threadId": h.thread})
		}
	}
}

func hostError(client *appserver.Client, err error) error {
	if err == nil {
		return nil
	}
	client.Diagnostics.Lock()
	defer client.Diagnostics.Unlock()
	if diagnostic := strings.TrimSpace(textsanitize.Safe(string(client.Diagnostics.Text), false)); diagnostic != "" {
		return fmt.Errorf("%w\nCodex: %s", err, diagnostic)
	}
	return err
}

func (h *client) message(m appserver.Message) error {
	if m.Method != "" && len(m.ID) != 0 {
		return fmt.Errorf("headless cannot answer host request %s; continue interactively", m.Method)
	}
	if m.Method == "" {
		request, ok := h.requests.Take(string(m.ID))
		if !ok {
			return nil
		}
		if m.Error != nil {
			return fmt.Errorf("%s: %s", request.Method, m.Error.Message)
		}
		switch request.Method {
		case "initialize":
			if err := h.requests.Notify("initialized", map[string]any{}); err != nil {
				return err
			}
			_, err := h.requests.Send("thread/start", map[string]any{"approvalPolicy": "never", "sandbox": "danger-full-access"}, "", struct{}{})
			return err
		case "thread/start":
			var result struct {
				Thread struct {
					ID  string `json:"id"`
					Cwd string `json:"cwd"`
				} `json:"thread"`
			}
			if err := json.Unmarshal(m.Result, &result); err != nil {
				return err
			}
			if result.Thread.ID == "" || result.Thread.Cwd == "" {
				return errors.New("headless thread/start returned no thread or workspace")
			}
			h.thread = result.Thread.ID
			_, err := h.requests.Send("turn/start", map[string]any{"threadId": h.thread, "input": appserver.Input(h.prompt)}, "", struct{}{})
			return err
		}
		return nil
	}
	switch m.Method {
	case "item/started", "item/completed":
		var event struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				Type      string           `json:"type"`
				Delivery  string           `json:"delivery"`
				Questions []jsontext.Value `json:"questions"`
			} `json:"item"`
		}
		if err := json.Unmarshal(m.Params, &event); err != nil {
			return err
		}
		if event.ThreadID == h.thread && event.Item.Type == "agentMessage" && event.Item.Delivery == "async" && len(event.Item.Questions) > 0 {
			return errors.New("headless cannot answer pending questions; continue interactively")
		}
	case "turn/started", "turn/completed":
		var event struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(m.Params, &event); err != nil {
			return err
		}
		if m.Method == "turn/completed" {
			h.notices.ObserveTurn(event.ThreadID, event.Turn.ID, event.Turn.Status)
		}
		if h.thread == "" || event.ThreadID != h.thread {
			return nil
		}
		if m.Method == "turn/started" {
			if event.Turn.ID == "" {
				return errors.New("headless turn has no identity")
			}
			h.turn, h.completed = event.Turn.ID, false
			return nil
		}
		if h.turn == "" || event.Turn.ID != h.turn {
			return nil
		}
		if event.Turn.Status != "completed" {
			return fmt.Errorf("headless turn %s: %s", event.Turn.ID, event.Turn.Status)
		}
		h.completed = true
	}
	return nil
}
