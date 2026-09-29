package appserver_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
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
	io.Copy(io.Discard, os.Stdin)
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
			c, err := appserver.Start(cmd)
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
			// Let the reader encounter more events with no available slots.
			select {
			case err := <-c.Done:
				t.Fatalf("full queue terminated app-server: %v", err)
			case <-time.After(50 * time.Millisecond):
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
