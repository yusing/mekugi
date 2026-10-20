package headless

import (
	"bufio"
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestHeadlessFailureHostProcess(t *testing.T) {
	scenario := os.Getenv("MEKUGI_HEADLESS_FAILURE_HOST")
	if scenario == "" {
		return
	}
	if scenario == "startup" {
		fmt.Fprintln(os.Stderr, "fixture: invalid model configuration")
		os.Exit(2)
	}
	marker, err := os.Create(os.Getenv("MEKUGI_HEADLESS_RPC_MARKER"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request appserver.Message
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(marker, request.Method)
		switch request.Method {
		case "initialize":
			fmt.Fprintf(os.Stdout, "{\"id\":%s,\"result\":{}}\n", request.ID)
		case "thread/start":
			fmt.Fprintf(os.Stdout, "{\"id\":%s,\"result\":{\"thread\":{\"id\":\"main\",\"cwd\":\"/workspace\"}}}\n", request.ID)
		case "turn/start":
			switch scenario {
			case "approval":
				fmt.Fprintln(os.Stdout, `{"id":"approval","method":"item/commandExecution/requestApproval","params":{"threadId":"main"}}`)
			case "question":
				fmt.Fprintln(os.Stdout, `{"method":"item/completed","params":{"threadId":"main","item":{"type":"agentMessage","delivery":"async","questions":[{"id":"question"}]}}}`)
			case "success":
				fmt.Fprintln(os.Stdout, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"turn"}}}`)
				fmt.Fprintln(os.Stdout, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"turn","status":"completed"}}}`)
				// The string ID cannot settle the numeric request, even after completion.
				fmt.Fprintf(os.Stdout, "{\"id\":%q,\"result\":{}}\n", string(request.ID))
				fmt.Fprintf(os.Stdout, "{\"id\":%s,\"result\":{}}\n", request.ID)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(marker, "eof")
	if err := marker.Close(); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestHeadlessAppServerStartupDiagnosticStaysOutOfJSONL(t *testing.T) {
	t.Setenv("MEKUGI_HEADLESS_FAILURE_HOST", "startup")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var output bytes.Buffer
	wait, err := Start(ctx, exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHeadlessFailureHostProcess$"), strings.NewReader("fixture"), &output, nil)
	if err == nil {
		err = wait()
	}
	if err == nil || !strings.Contains(err.Error(), "fixture: invalid model configuration") {
		t.Fatalf("missing startup diagnostic: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("startup stderr leaked into JSONL: %s", &output)
	}
}

func TestHeadlessAppServerStreamAndShutdown(t *testing.T) {
	outputFailure := errors.New("output failed")
	for _, scenario := range []string{"success", "approval", "question", "output", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("MEKUGI_HEADLESS_FAILURE_HOST", scenario)
			marker := filepath.Join(t.TempDir(), "rpc")
			t.Setenv("MEKUGI_HEADLESS_RPC_MARKER", marker)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHeadlessFailureHostProcess$")
			// Leave graceful shutdown to the client, including context cancellation.
			cmd.Cancel = func() error { return nil }
			var output bytes.Buffer
			var writer io.Writer = &output
			if scenario == "output" {
				writer = headlessFailedIO{outputFailure}
			} else if scenario == "cancel" {
				writer = cancelWriter{&output, cancel}
			}
			wait, err := Start(ctx, cmd, strings.NewReader("fixture"), writer, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = wait()
			if scenario == "success" && err != nil || scenario != "success" && err == nil {
				t.Fatalf("run error=%v", err)
			}
			if scenario == "output" && !errors.Is(err, outputFailure) || scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost original failure: %v", err)
			}
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				t.Fatal("run returned before host shutdown")
			}
			requests, readErr := os.ReadFile(marker)
			if readErr != nil || !strings.HasSuffix(string(requests), "eof\n") {
				t.Fatalf("missing graceful shutdown: %s (%v)", requests, readErr)
			}
			if scenario == "output" && string(requests) != "initialize\neof\n" {
				t.Fatalf("interpreted undelivered host message: %s", requests)
			}
			var methods []string
			var ids []string
			for _, row := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
				if len(row) == 0 {
					continue
				}
				var message appserver.Message
				if err := json.Unmarshal(row, &message); err != nil {
					t.Fatal(err)
				}
				methods = append(methods, message.Method)
				ids = append(ids, string(message.ID))
			}
			switch scenario {
			case "success":
				if !slices.Equal(methods, []string{"", "", "turn/started", "turn/completed", "", "", "mekugi/headless/completed"}) || !slices.Equal(ids, []string{"1", "2", "", "", `"3"`, "3", ""}) {
					t.Fatalf("completion before exact response: %s", &output)
				}
			case "approval", "question":
				want := "item/commandExecution/requestApproval"
				if scenario == "question" {
					want = "item/completed"
				}
				if !slices.Equal(methods, []string{"", "", want}) || !strings.Contains(err.Error(), "continue interactively") {
					t.Fatalf("host request not emitted before rejection: %s (%v)", &output, err)
				}
			}
		})
	}
}

type cancelWriter struct {
	io.Writer
	cancel context.CancelFunc
}

func (w cancelWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.cancel()
	return n, err
}
