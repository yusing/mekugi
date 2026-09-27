package appserver_test

import (
	"context"
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
	io.Copy(io.Discard, os.Stdin)
	fmtText := `{"method":"test/drained","params":{}}` + "\n"
	io.WriteString(os.Stdout, fmtText)
	os.Exit(0)
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
