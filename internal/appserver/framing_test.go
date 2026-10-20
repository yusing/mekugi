package appserver_test

import (
	"context"
	json "encoding/json/v2"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

// Larger than the former 16 MiB Scanner ceiling, including within one item.
const largeHistoryTextSize = 17 << 20

func TestAppServerFramingChild(t *testing.T) {
	mode := os.Getenv("MEKUGI_APP_SERVER_FRAMING_TEST")
	if mode == "" {
		return
	}
	write := func(text string) {
		// Neither pipe writes nor reader buffer boundaries are RPC boundaries.
		for len(text) > 0 {
			n := min(len(text), 4093)
			if _, err := io.WriteString(os.Stdout, text[:n]); err != nil {
				os.Exit(2)
			}
			text = text[n:]
		}
	}
	switch mode {
	case "result", "params":
		write("{\"method\":\"test/before\"}\n")
		if mode == "result" {
			write(`{"id":7,"result":`)
		} else {
			write(`{"method":"thread/started","params":`)
		}
		write(`{"thread":{"id":"resumed-thread","turns":[{"items":[{"type":"agentMessage","text":"`)
		write(strings.Repeat("x", largeHistoryTextSize))
		write(`"}]}]}}}` + "\n")
		write("{\"method\":\"test/after\"}\n")
	case "lines":
		write("{\"method\":\"test/lf\",\"params\":{\"text\":\"escaped\\nnewline\"}}\n{\"id\":\"reply\",\"result\":{}}\r\n")
	case "final":
		write(`{"method":"test/final"}`)
	case "empty":
	case "truncated":
		write(`{"result":{"thread":`)
	case "malformed":
		write("not JSON\n")
	case "blank":
		write("\n")
	case "joined":
		write("{\"method\":\"first\"}{\"method\":\"second\"}\n")
	case "partial":
		write("{\"method\":\"test/before\"}\n{\"result\":\"")
		write(strings.Repeat("x", 128<<10))
	default:
		os.Exit(3)
	}
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func framingClient(t *testing.T, mode string) (*appserver.Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppServerFramingChild$")
	cmd.Env = append(os.Environ(), "MEKUGI_APP_SERVER_FRAMING_TEST="+mode)
	c, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, ctx
}

func framingMessage(t *testing.T, c *appserver.Client, ctx context.Context) appserver.Message {
	t.Helper()
	select {
	case m, ok := <-c.Messages:
		if !ok {
			t.Fatalf("RPC stream closed early: %v", <-c.Done)
		}
		return m
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return appserver.Message{}
	}
}

func TestAppServerLargeHistoryFrame(t *testing.T) {
	for _, mode := range []string{"result", "params"} {
		t.Run(mode, func(t *testing.T) {
			c, ctx := framingClient(t, mode)
			if err := c.Input.Close(); err != nil {
				t.Fatal(err)
			}
			if m := framingMessage(t, c, ctx); m.Method != "test/before" {
				t.Fatalf("first event: %q", m.Method)
			}
			m := framingMessage(t, c, ctx)
			data := m.Result
			if mode == "result" {
				if string(m.ID) != "7" || m.Method != "" {
					t.Fatalf("response envelope: id=%s method=%q", m.ID, m.Method)
				}
			} else {
				if len(m.ID) != 0 || m.Method != "thread/started" {
					t.Fatalf("notification envelope: id=%s method=%q", m.ID, m.Method)
				}
				data = m.Params
			}
			var history struct {
				Thread struct {
					ID    string `json:"id"`
					Turns []struct {
						Items []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"items"`
					} `json:"turns"`
				} `json:"thread"`
			}
			if err := json.Unmarshal(data, &history); err != nil {
				t.Fatal(err)
			}
			if history.Thread.ID != "resumed-thread" || len(history.Thread.Turns) != 1 || len(history.Thread.Turns[0].Items) != 1 {
				t.Fatal("history structure was lost")
			}
			item := history.Thread.Turns[0].Items[0]
			if item.Type != "agentMessage" || len(item.Text) != largeHistoryTextSize || strings.Trim(item.Text, "x") != "" {
				t.Fatalf("history item corrupted: type=%q text bytes=%d", item.Type, len(item.Text))
			}
			if m := framingMessage(t, c, ctx); m.Method != "test/after" {
				t.Fatalf("last event: %q", m.Method)
			}
			if err := <-c.Done; err != nil {
				t.Fatal(err)
			}
			if _, ok := <-c.Messages; ok {
				t.Fatal("unexpected extra event")
			}
		})
	}
}

func TestAppServerFrameBoundaries(t *testing.T) {
	for _, mode := range []string{"lines", "final", "empty", "truncated", "malformed", "blank", "joined"} {
		t.Run(mode, func(t *testing.T) {
			c, ctx := framingClient(t, mode)
			if err := c.Input.Close(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "lines":
				m := framingMessage(t, c, ctx)
				if m.Method != "test/lf" || string(m.Params) != `{"text":"escaped\nnewline"}` {
					t.Fatal("LF frame or escaped newline was not preserved")
				}
				m = framingMessage(t, c, ctx)
				if string(m.ID) != `"reply"` || string(m.Result) != `{}` {
					t.Fatal("CRLF response was not preserved")
				}
			case "final":
				if m := framingMessage(t, c, ctx); m.Method != "test/final" {
					t.Fatal("complete final frame was lost")
				}
			}
			select {
			case err := <-c.Done:
				invalid := mode == "truncated" || mode == "malformed" || mode == "blank" || mode == "joined"
				if invalid {
					if err == nil || !strings.Contains(err.Error(), "decode app-server") {
						t.Fatalf("missing protocol error: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if m, ok := <-c.Messages; ok {
				t.Fatalf("unexpected event: method=%q id=%s", m.Method, m.ID)
			}
		})
	}
}

func TestAppServerClosePartialFrame(t *testing.T) {
	c, ctx := framingClient(t, "partial")
	if m := framingMessage(t, c, ctx); m.Method != "test/before" {
		t.Fatal("missing initial event")
	}
	c.Close()
	select {
	case <-c.Done:
	case <-ctx.Done():
		t.Fatal("close left a partial-frame read blocked")
	}
	if m, ok := <-c.Messages; ok {
		t.Fatalf("partial frame delivered: %q", m.Method)
	}
}
