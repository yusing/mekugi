package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
)

// Headless is an app-server client, not a second task executor. Its JSONL stream
// retains host messages verbatim as objects, with namespaced frontend events.
func startHeadlessAppServer(ctx context.Context, cmd *exec.Cmd, input io.Reader, output io.Writer, proxy *mekugiProxy) (func() error, error) {
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
	initializeID, err := c.Initialize()
	if err != nil {
		c.Close()
		<-c.Done
		return nil, headlessHostError(c, err)
	}
	h := &headlessAppServer{ctx: ctx, client: c, proxy: proxy, output: jsontext.NewEncoder(output), prompt: prompt, requestID: initializeID, request: "initialize"}
	return h.run, nil
}

type headlessAppServer struct {
	ctx                        context.Context
	client                     *appserver.Client
	proxy                      *mekugiProxy
	output                     *jsontext.Encoder
	prompt, request, requestID string
	thread, turn               string
	completed                  bool
	reset                      *journalResetDriver
	resetPhase                 string
}

func (h *headlessAppServer) emit(method string, params any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.MarshalEncode(h.output, &appserver.Message{Method: method, Params: data})
}

func (h *headlessAppServer) send(method string, params any) error {
	id, err := h.client.Send(method, params, true)
	if err == nil {
		h.request, h.requestID = method, id
	}
	return err
}

func (h *headlessAppServer) run() (runErr error) {
	exited := false
	defer func() {
		defer h.client.Input.Close()
		if !exited {
			runErr = errors.Join(runErr, h.client.Shutdown())
		}
		runErr = headlessHostError(h.client, runErr)
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return h.ctx.Err()
		case err := <-h.client.Done:
			exited = true
			return errors.Join(errors.New("headless app-server disconnected before completion"), err)
		case m, ok := <-h.client.Messages:
			if !ok {
				return errors.New("headless app-server output ended before completion")
			}
			if err := json.MarshalEncode(h.output, &m); err != nil {
				return err
			}
			if err := h.message(m); err != nil {
				return err
			}
		case now := <-ticker.C:
			if h.reset != nil {
				if err := h.reset.tick(now); err != nil {
					return err
				}
			}
		}
		if h.reset != nil {
			if h.reset.notice != "" && !h.reset.active() {
				return errors.New(h.reset.notice)
			}
			if phase := h.reset.phase; phase != h.resetPhase {
				h.resetPhase = phase
				if err := h.emit("mekugi/journal/reset", map[string]any{"threadId": h.thread, "phase": phase}); err != nil {
					return err
				}
			}
		}
		if h.completed && h.requestID == "" && !h.reset.active() {
			return h.emit("mekugi/headless/completed", map[string]any{"threadId": h.thread})
		}
	}
}

func headlessHostError(client *appserver.Client, err error) error {
	if err == nil {
		return nil
	}
	client.Diagnostics.Lock()
	defer client.Diagnostics.Unlock()
	if diagnostic := strings.TrimSpace(livediff.Safe(string(client.Diagnostics.Text), false)); diagnostic != "" {
		return fmt.Errorf("%w\nCodex: %s", err, diagnostic)
	}
	return err
}

func (h *headlessAppServer) message(m appserver.Message) error {
	if handled, err := h.reset.message(m); handled || err != nil {
		return err
	}
	if m.Method != "" && len(m.ID) != 0 {
		return fmt.Errorf("headless cannot answer host request %s; continue interactively", m.Method)
	}
	if m.Method == "item/started" || m.Method == "item/completed" {
		var p appServerEvent
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.ThreadID == h.thread && p.Item.Type == "agentMessage" && p.Item.Delivery == "async" && len(p.Item.Questions) > 0 {
			return errors.New("headless cannot answer pending questions; continue interactively")
		}
	}
	if m.Method == "" && string(m.ID) == h.requestID {
		method := h.request
		h.request, h.requestID = "", ""
		if m.Error != nil {
			return fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		switch method {
		case "initialize":
			if _, err := h.client.Send("initialized", map[string]any{}, false); err != nil {
				return err
			}
			return h.send("thread/start", map[string]any{"approvalPolicy": "never", "sandbox": "danger-full-access"})
		case "thread/start":
			var result struct {
				Thread appServerThreadInfo `json:"thread"`
			}
			if err := json.Unmarshal(m.Result, &result); err != nil {
				return err
			}
			if result.Thread.ID == "" || result.Thread.Cwd == "" {
				return errors.New("headless thread/start returned no thread or workspace")
			}
			h.thread = result.Thread.ID
			h.reset = &journalResetDriver{ctx: h.ctx, proxy: h.proxy, client: h.client, workspace: result.Thread.Cwd, thread: h.thread}
			return h.send("turn/start", map[string]any{"threadId": h.thread, "input": appserver.Input(h.prompt)})
		}
		return nil
	}
	if m.Method != "turn/started" && m.Method != "turn/completed" {
		return nil
	}
	var p appServerEvent
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return err
	}
	if h.thread == "" {
		return nil
	}
	if p.ThreadID != h.thread {
		if err := h.proxy.observeJournalHostTurn(h.ctx, h.reset.workspace, m.Method, p); err != nil {
			return h.emit("mekugi/journal/notice", map[string]any{"threadId": p.ThreadID, "message": err.Error()})
		}
		return nil
	}
	if m.Method == "turn/started" {
		if p.Turn.ID == "" {
			return errors.New("headless turn has no identity")
		}
		h.turn, h.completed = p.Turn.ID, false
		return nil
	}
	if p.Turn.ID != h.turn {
		return nil
	}
	if p.Turn.Status != "completed" {
		if p.Turn.Status == "interrupted" {
			if err := h.reset.stop(p.Turn.ID); err != nil {
				return err
			}
		}
		return fmt.Errorf("headless turn %s: %s", p.Turn.ID, p.Turn.Status)
	}
	h.completed = true
	return h.reset.completed(p.Turn.ID)
}
