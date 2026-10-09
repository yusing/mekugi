package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

func TestHeadlessFailureHostProcess(t *testing.T) {
	mode := os.Getenv("MEKUGI_HEADLESS_FAILURE_HOST")
	if mode == "" {
		return
	}
	if mode == "startup" {
		fmt.Fprintln(os.Stderr, "fixture: invalid model configuration")
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request appserver.Message
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		switch request.Method {
		case "thread/compact/start":
			if mode == "failed" || mode == "interrupted" {
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{}}`+"\n", request.ID)
				fmt.Fprintf(os.Stdout, `{"method":"turn/started","params":{"threadId":%q,"turn":{"id":"compact"}}}`+"\n", os.Getenv("MEKUGI_HEADLESS_FAILURE_THREAD"))
				fmt.Fprintf(os.Stdout, `{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":"compact","status":%q}}}`+"\n", os.Getenv("MEKUGI_HEADLESS_FAILURE_THREAD"), mode)
			} else {
				fmt.Fprintf(os.Stdout, `{"id":%s,"error":{"code":-1,"message":"fixture compaction rejected"}}`+"\n", request.ID)
			}
		default:
			os.Exit(5)
		}
	}
	os.Exit(0)
}

func TestHeadlessAppServerReturnsResetFailure(t *testing.T) {
	for _, mode := range []string{"rejected", "failed", "interrupted"} {
		t.Run(mode, func(t *testing.T) { testHeadlessResetFailure(t, mode) })
	}
}

func testHeadlessResetFailure(t *testing.T, mode string) {
	t.Setenv("MEKUGI_HEADLESS_FAILURE_HOST", mode)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	d, _ := resetDriverFixture(t, "slice")
	t.Setenv("MEKUGI_HEADLESS_FAILURE_THREAD", d.thread)
	client, err := appserver.Start(exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHeadlessFailureHostProcess$"))
	if err != nil {
		t.Fatal(err)
	}
	d.ctx, d.client, d.deadline = ctx, client, time.Now()
	var output bytes.Buffer
	h := &headlessAppServer{ctx: ctx, client: client, proxy: d.proxy, output: jsontext.NewEncoder(&output), thread: d.thread, turn: "first-turn", completed: true, reset: d}
	err = h.run()
	if err == nil || mode == "rejected" && !strings.Contains(err.Error(), "fixture compaction rejected") {
		t.Fatalf("run error: %v", err)
	}
	intent, readErr := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if readErr != nil || intent != nil || d.active() {
		t.Fatalf("failed reset still active: intent=%+v active=%t err=%v", intent, d.active(), readErr)
	}
	if bytes.Contains(output.Bytes(), []byte("mekugi/headless/completed")) {
		t.Fatalf("failed run claimed completion: %s", &output)
	}

}

func TestHeadlessAppServerStartupDiagnosticStaysOutOfJSONL(t *testing.T) {
	t.Setenv("MEKUGI_HEADLESS_FAILURE_HOST", "startup")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var output bytes.Buffer
	wait, err := startHeadlessAppServer(ctx, exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHeadlessFailureHostProcess$"), strings.NewReader("fixture"), &output, nil, nil)
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
