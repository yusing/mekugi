package appserver_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestAppServerShutdownChild(t *testing.T) {
	mode := os.Getenv("MEKUGI_APP_SERVER_SHUTDOWN_TEST")
	if mode == "" {
		return
	}
	if mode == "stuck" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "burst" {
		for i := range 4096 {
			if _, err := fmt.Fprintf(os.Stdout, "{\"method\":\"test/event/%d\"}\n", i); err != nil {
				os.Exit(2)
			}
		}
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if mode == "cancel" {
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		io.WriteString(os.Stdout, "{\"method\":\"test/ready\"}\n")
		<-interrupt
	} else {
		io.Copy(io.Discard, os.Stdin)
	}
	fmtText := `{"method":"test/drained","params":{}}` + "\n"
	io.WriteString(os.Stdout, fmtText)
	os.Exit(0)
}

func TestAppServerBurst(t *testing.T) {
	for _, action := range []string{"drain", "close", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppServerShutdownChild$")
			cmd.Env = append(os.Environ(), "MEKUGI_APP_SERVER_SHUTDOWN_TEST=burst")
			observed := make(chan struct{}, 1)
			c, err := appserver.Start(cmd, func(message appserver.Message) {
				if message.Method == "test/event/256" {
					observed <- struct{}{}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(c.Close)
			// Stop consuming until the bounded channel is full. On the old
			// implementation this burst either killed the child already or
			// will have lost events when draining below.
			for len(c.Messages) < cap(c.Messages) {
				select {
				case err := <-c.Done:
					t.Fatalf("burst terminated app-server: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(time.Millisecond):
				}
			}
			// Observation must not wait for the full presentation queue to drain.
			select {
			case <-observed:
			case err := <-c.Done:
				t.Fatalf("full queue terminated app-server: %v", err)
			case <-ctx.Done():
				t.Fatal("observer waited for presentation")
			}
			switch action {
			case "drain":
				for i := range 4096 {
					select {
					case m, ok := <-c.Messages:
						if !ok || m.Method != fmt.Sprintf("test/event/%d", i) {
							t.Fatalf("event %d: got %q, open=%v", i, m.Method, ok)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				if err := c.Shutdown(); err != nil {
					t.Fatal(err)
				}
				if m, ok := <-c.Messages; ok {
					t.Fatalf("unexpected extra event: %+v", m)
				}
			case "close":
				c.Close()
				select {
				case <-c.Done:
				case <-ctx.Done():
					t.Fatal("close left the reader blocked")
				}
			case "shutdown":
				result := make(chan error, 1)
				go func() { result <- c.Shutdown() }()
				select {
				case err := <-result:
					if err == nil || !strings.Contains(err.Error(), "forced termination") {
						t.Fatalf("missing bounded shutdown failure: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("shutdown left the reader blocked")
				}
			}
		})
	}
}

func TestAppServerShutdown(t *testing.T) {
	for _, mode := range []string{"graceful", "stuck"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppServerShutdownChild$")
			cmd.Env = append(os.Environ(), "MEKUGI_APP_SERVER_SHUTDOWN_TEST="+mode)
			c, err := appserver.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			err = c.Shutdown()
			if mode == "stuck" {
				if err == nil || !strings.Contains(err.Error(), "forced termination") {
					t.Fatalf("missing forced-shutdown failure: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			m, ok := <-c.Messages
			if !ok || m.Method != "test/drained" {
				t.Fatal("did not let backend drain on EOF")
			}
		})
	}
}

func TestAppServerCancellationDrainsOwnedWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppServerShutdownChild$")
	cmd.Env = append(os.Environ(), "MEKUGI_APP_SERVER_SHUTDOWN_TEST=cancel")
	c, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	select {
	case message := <-c.Messages:
		if message.Method != "test/ready" {
			t.Fatalf("unexpected fixture startup: %+v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host did not become ready for cancellation")
	}
	cancel()
	select {
	case message, open := <-c.Messages:
		if !open || message.Method != "test/drained" {
			t.Fatal("context cancellation preempted host interruption cleanup")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("context cancellation did not request host interruption")
	}
	if err := c.Shutdown(); !errors.Is(err, context.Canceled) {
		t.Fatalf("joined cancellation lost its cause: %v", err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatal("host cleanup did not finish before the cancellation joined")
	}
}
