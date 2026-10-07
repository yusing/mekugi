// Package claude adapts the official Agent SDK bridge, never Codex protocols.
package claude

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/session"
)

const frameLimit = 8 << 20

type ObservationEndpoint struct {
	Socket            string         `json:"socket"`
	Token             string         `json:"token"`
	Plugin            string         `json:"plugin,omitempty"`
	FrontendDirectory string         `json:"frontendDirectory,omitempty"`
	JournalSchema     jsontext.Value `json:"journalSchema,omitempty"`
	BashEnv           string         `json:"bashEnv,omitempty"`
}

type Config struct {
	Environment []string             `json:"-"`
	Companion   *ObservationEndpoint `json:"companion,omitempty"`
	Cwd         string               `json:"cwd"`
	Executable  string               `json:"executable"`
	Resume      string               `json:"resume,omitempty"`
	ForkSession bool                 `json:"forkSession,omitzero"`
	Model       string               `json:"model,omitempty"`
}
type Client struct {
	cmd       *exec.Cmd
	input     io.WriteCloser
	events    chan session.Event
	done      chan struct{}
	cancel    context.CancelFunc
	mu        sync.Mutex
	waitErr   error
	closeOnce sync.Once
	adapter   adapter
}

func Start(ctx context.Context, node, bridge string, config Config) (*Client, error) {
	if config.ForkSession && config.Resume == "" {
		return nil, errors.New("native session fork requires a resume session ID")
	}
	launch := struct {
		Config
		CompanionFD int `json:"companionFD,omitzero"`
	}{Config: config}
	launch.Companion = nil // Capabilities never enter argv, environment or host settings.
	var endpointPipe *os.File
	if config.Companion != nil {
		reader, writer, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		endpointPipe = reader
		defer reader.Close()
		data, err := json.Marshal(config.Companion)
		if err == nil {
			_, err = writer.Write(data)
		}
		closeErr := writer.Close()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		launch.CompanionFD = 3
	}
	payload, err := json.Marshal(launch)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, node, bridge, string(payload))
	cmd.Dir = config.Cwd
	cmd.Env = config.Environment
	if config.Companion != nil && config.Companion.BashEnv != "" {
		// A fresh native launch owns a new observer. Do not inherit the calling
		// command's nested-shell guard; its own command shells set it normally.
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = slices.DeleteFunc(slices.Clone(cmd.Env), func(value string) bool {
			return strings.HasPrefix(value, execsegment.Guard+"=")
		})
	}
	if endpointPipe != nil {
		cmd.ExtraFiles = []*os.File{endpointPipe}
	}
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		input.Close()
		return nil, err
	}
	// Native stderr is diagnostic only. Do not copy credentials/configuration into
	// UI activity or retained stores. The structured error supplies user feedback.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		input.Close()
		output.Close()
		return nil, err
	}
	c := &Client{cmd: cmd, input: input, events: make(chan session.Event, 64), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(c.done)
		defer close(c.events)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), frameLimit)
		var readErr error
		for scanner.Scan() {
			events, err := c.adapter.decode(scanner.Bytes())
			if err != nil {
				readErr = err
				cancel()
				break
			}
			for _, event := range events {
				select {
				case c.events <- event:
				case <-ctx.Done():
					cancel()
					goto finished
				}
			}
		}
		if scanner.Err() != nil {
			readErr = scanner.Err()
			cancel()
		}
	finished:
		c.waitErr = errors.Join(readErr, cmd.Wait())
		if c.waitErr != nil && (readErr != nil || ctx.Err() == nil) {
			select {
			case c.events <- session.Event{Kind: "error", Text: fmt.Sprintf("Claude bridge stopped: %v", c.waitErr)}:
			default:
			}
		}
	}()
	return c, nil
}
func (c *Client) Events() <-chan session.Event { return c.events }
func (c *Client) send(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload)+1 > frameLimit {
		return errors.New("input exceeds the 8 MiB bridge frame limit")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.input.Write(append(payload, '\n'))
	return err
}
func (c *Client) Send(ctx context.Context, text string) error {
	return c.send(ctx, map[string]any{"kind": "input", "text": text})
}
func (c *Client) Respond(ctx context.Context, d session.Decision) error {
	value := map[string]any{"kind": "decision", "id": d.ID, "allow": d.Allow}
	if d.Answers != nil {
		value["answers"] = d.Answers
	}
	return c.send(ctx, value)
}
func (c *Client) Interrupt(ctx context.Context) error {
	return c.send(ctx, map[string]string{"kind": "interrupt"})
}
func (c *Client) SetSettings(ctx context.Context, s session.Settings) error {
	return c.send(ctx, map[string]string{"kind": "settings", "id": s.ID, "field": s.Field, "value": s.Value})
}
func (c *Client) StopTask(ctx context.Context, id string) error {
	return c.send(ctx, map[string]string{"kind": "stop_task", "id": id})
}
func (c *Client) SendAgentMessage(ctx context.Context, message session.AgentMessage) error {
	return c.send(ctx, map[string]string{"kind": "agent_message", "id": message.ID, "sessionID": message.SessionID, "agentID": message.AgentID, "text": message.Text})
}
func (c *Client) RunShell(ctx context.Context, command session.ShellCommand) error {
	return c.send(ctx, map[string]string{"kind": "shell", "id": command.ID, "sessionID": command.SessionID, "text": command.Command})
}
func (c *Client) Reset(ctx context.Context, id string) error {
	return c.send(ctx, map[string]string{"kind": "reset", "id": id})
}
func (c *Client) RenameSession(ctx context.Context, title session.SessionTitle) error {
	return c.send(ctx, map[string]string{"kind": "title", "id": title.ID, "sessionID": title.SessionID, "title": title.Title})
}
func (c *Client) ListSessions(ctx context.Context, request session.SessionListRequest) error {
	return c.send(ctx, map[string]any{"kind": "sessions", "id": request.ID, "cwd": request.Cwd, "cursor": request.Cursor, "limit": request.Limit})
}
func (c *Client) ChangeSession(ctx context.Context, change session.SessionChange) error {
	return c.send(ctx, map[string]string{"kind": "session_change", "id": change.ID, "sessionID": change.SessionID, "cwd": change.Cwd})
}
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.input.Close()
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
			c.cancel()
			<-c.done
		}
		c.cancel()
	})
	return c.waitErr
}

type content struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Text      string         `json:"text"`
	ToolUseID string         `json:"tool_use_id"`
	Input     jsontext.Value `json:"input"`
	Content   jsontext.Value `json:"content"`
	IsError   bool           `json:"is_error"`
}
type nativeEvent struct {
	session.Usage
	Commands       []session.Command  `json:"commands"`
	Limit          *session.RateLimit `json:"rate_limit_info"`
	SubagentType   string             `json:"subagent_type"`
	TaskID         string             `json:"task_id"`
	ToolUseID      string             `json:"tool_use_id"`
	TaskType       string             `json:"task_type"`
	Description    string             `json:"description"`
	Status         string             `json:"status"`
	Summary        string             `json:"summary"`
	Ambient        bool               `json:"ambient"`
	SkipTranscript bool               `json:"skip_transcript"`
	StartupFailure string             `json:"startup_failure_reason"`
	Patch          struct {
		Status      string `json:"status"`
		Description string `json:"description"`
		Error       string `json:"error"`
	} `json:"patch"`
	Content   string   `json:"content"`
	UUID      string   `json:"uuid"`
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype"`
	SessionID string   `json:"session_id"`
	Model     string   `json:"model"`
	Parent    string   `json:"parent_tool_use_id"`
	IsError   bool     `json:"is_error"`
	Errors    []string `json:"errors"`
	Message   struct {
		ID      string         `json:"id"`
		Content jsontext.Value `json:"content"`
	} `json:"message"`
	ToolResult jsontext.Value `json:"tool_use_result"`
	Event      struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		Block content `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	} `json:"event"`
}
