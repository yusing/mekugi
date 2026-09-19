package toolplugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInvocationFormatterIsLazyAndReusesHost(t *testing.T) {
	node, root := newFormatterFixture(t)
	ctx, closeFormatter := WithOutputFormatter(t.Context(), node, root)
	t.Cleanup(closeFormatter)
	formatter := formatterFromContext(ctx, node, root)
	if formatter == nil || formatter.process != nil {
		t.Fatal("formatter missing or started eagerly")
	}
	first, err := FormatOutput(ctx, node, root, []string{"3", "head", "hello world", "diagnostic"})
	if err != nil {
		t.Fatal(err)
	}
	process := formatter.process
	batch, err := FormatOutputBatch(ctx, node, root, [][]string{
		{"1", "head", "alpha", ""},
		{"1", "tail", "beta", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if formatter.process != process {
		t.Fatal("formatter host was not reused")
	}
	if first.Stdout != "hello" || len(batch) != 2 || batch[0].Stdout != "alpha" || batch[1].Stdout != "beta" {
		t.Fatalf("unexpected formatting: %+v, %+v", first, batch)
	}
}

func TestInvocationFormatterRootMismatchUsesOneShot(t *testing.T) {
	node, root := newFormatterFixture(t)
	ctx, closeFormatter := WithOutputFormatter(t.Context(), node, root)
	t.Cleanup(closeFormatter)
	formatter := formatterFromContext(ctx, node, root)
	otherNode, otherRoot := newFormatterFixture(t)
	result, err := FormatOutput(ctx, otherNode, otherRoot, []string{"1", "head", "value", ""})
	if err != nil || result.Stdout != "value" {
		t.Fatalf("one-shot mismatch = %+v, %v", result, err)
	}
	if formatter.process != nil {
		t.Fatal("root mismatch started invocation formatter")
	}
}

func TestInvocationFormatterQueuedCancellationAndClose(t *testing.T) {
	node, root := newFormatterFixture(t)
	ctx, closeFormatter := WithOutputFormatter(t.Context(), node, root)
	formatter := formatterFromContext(ctx, node, root)
	formatter.gate <- struct{}{}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := FormatOutput(cancelled, node, root, []string{"1", "head", "", ""}); err == nil {
		t.Fatal("queued canceled call succeeded")
	}
	<-formatter.gate
	if _, err := FormatOutput(ctx, node, root, []string{"1", "head", "value", ""}); err != nil {
		t.Fatal(err)
	}
	process := formatter.process
	closeFormatter()
	select {
	case <-process.done:
	default:
		t.Fatal("cleanup did not reap formatter host")
	}
	if _, err := FormatOutput(ctx, node, root, []string{"1", "head", "", ""}); err == nil {
		t.Fatal("closed formatter accepted call")
	}
}

func newFormatterLifecycleFixture(t *testing.T) (string, string) {
	t.Helper()
	node, root := newFormatterFixture(t)
	host := `import {appendFileSync, writeFileSync} from "node:fs";
import {createInterface} from "node:readline";
const lines = createInterface({input: process.stdin, crlfDelay: Infinity});
let ready = false;
for await (const line of lines) {
  const request = JSON.parse(line);
  if (!ready) {
    ready = true;
    appendFileSync(new URL("./starts", import.meta.url), "start\n");
    process.stdout.write('{"ready":true}\n');
    continue;
  }
  const mode = request.arguments[2];
  appendFileSync(new URL("./attempts", import.meta.url), mode + "\n");
  if (mode === "hang") {
    writeFileSync(new URL("./active", import.meta.url), "active");
    await new Promise(() => {});
  }
  if (mode === "malformed") {
    process.stdout.write("{malformed\n");
    continue;
  }
  process.stdout.write(JSON.stringify({stdout: mode, stderr: "", exitCode: 0}) + "\n");
}`
	if err := os.WriteFile(filepath.Join(root, hostFilename), []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	return node, root
}

func waitForFormatterMarker(t *testing.T, root, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("formatter marker %q was not created", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInvocationFormatterActiveCancellationReapsHost(t *testing.T) {
	node, root := newFormatterLifecycleFixture(t)
	ctx, closeFormatter := WithOutputFormatter(t.Context(), node, root)
	t.Cleanup(closeFormatter)
	callCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := FormatOutput(callCtx, node, root, []string{"1", "head", "hang", ""})
		result <- err
	}()
	waitForFormatterMarker(t, root, "active")
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation = %v", err)
	}
	if formatterFromContext(ctx, node, root).process != nil {
		t.Fatal("canceled formatter host was retained")
	}
}

func TestInvocationFormatterProtocolFailureDoesNotReplayAndNextCallRecovers(t *testing.T) {
	node, root := newFormatterLifecycleFixture(t)
	ctx, closeFormatter := WithOutputFormatter(t.Context(), node, root)
	t.Cleanup(closeFormatter)
	formatter := formatterFromContext(ctx, node, root)
	if _, err := FormatOutput(ctx, node, root, []string{"1", "head", "malformed", ""}); err == nil {
		t.Fatal("malformed formatter response succeeded")
	}
	failed := formatter.process
	if failed != nil {
		t.Fatal("failed formatter host was retained")
	}
	result, err := FormatOutput(ctx, node, root, []string{"1", "head", "next", ""})
	if err != nil || result.Stdout != "next" {
		t.Fatalf("recovered formatter = %+v, %v", result, err)
	}
	attempts, err := os.ReadFile(filepath.Join(root, "attempts"))
	if err != nil || string(attempts) != "malformed\nnext\n" {
		t.Fatalf("formatter requests were replayed: %q, %v", attempts, err)
	}
	starts, err := os.ReadFile(filepath.Join(root, "starts"))
	if err != nil || string(starts) != "start\nstart\n" {
		t.Fatalf("formatter host restart count = %q, %v", starts, err)
	}
}
