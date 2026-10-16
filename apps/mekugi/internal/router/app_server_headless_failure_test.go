package router

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHeadlessFailureHostProcess(t *testing.T) {
	if os.Getenv("MEKUGI_HEADLESS_FAILURE_HOST") == "startup" {
		fmt.Fprintln(os.Stderr, "fixture: invalid model configuration")
		os.Exit(2)
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
