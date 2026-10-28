package claude

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const transportFixture = `
import { createInterface } from 'node:readline';
const lines = createInterface({input:process.stdin});
const send = v => process.stdout.write(JSON.stringify(v)+'\n');
lines.on('line', line => {
 const v=JSON.parse(line);
 if(v.kind==='response') { send({kind:'event',event:{type:'callback',result:v.result,error:v.error}}); return; }
 if(v.method==='initialize') { send({kind:'response',id:v.id,result:{hooks_applied:true}}); return; }
 if(v.method==='block') return;
 if(v.method==='hook') { send({kind:'request',id:'hook',method:'hook',params:{tool_use_id:'native'}}); }
 if(v.method==='cancel') { send({kind:'cancel',id:'hook'}); }
 if(v.method==='stderr') process.stderr.write('x'.repeat(100000));
 send({kind:'response',id:v.id,result:v.params??{}});
 if(v.method==='shutdown') { lines.close();process.stdin.destroy(); }
});
`

func fixtureConfig(t *testing.T) Config {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("Bun is required for subprocess transport checks")
	}
	return Config{BunPath: bun, ClaudePath: bun, Workspace: t.TempDir(), Env: os.Environ(),
		OnRequest: func(context.Context, string, jsontext.Value) (jsontext.Value, error) {
			return jsontext.Value(`{}`), nil
		}}
}

func TestClientCorrelatedRequestsAndCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, err := start(ctx, fixtureConfig(t), []byte(transportFixture))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			params, err := json.Marshal(i)
			if err != nil {
				t.Error(err)
				return
			}
			got, err := c.Request(ctx, "echo", params)
			if err != nil {
				t.Error(err)
				return
			}
			if string(got) != string(params) {
				t.Errorf("response = %s, want %s", got, params)
			}
		})
	}
	wg.Wait()
	if _, err := c.Request(ctx, "stderr", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := c.Diagnostics(); len(got) != 64*1024 || strings.Trim(got, "x") != "" {
		t.Fatalf("diagnostic tail = %d bytes", len(got))
	}
	if _, open := <-c.Events(); open {
		t.Fatal("events remain open after joined shutdown")
	}
	if c.cmd.ProcessState == nil || !c.cmd.ProcessState.Exited() {
		t.Fatal("worker was not joined")
	}
}

func TestClientCallbackCancellationAndUncertainRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cfg := fixtureConfig(t)
	entered, stopped := make(chan struct{}), make(chan struct{})
	cfg.OnRequest = func(ctx context.Context, method string, params jsontext.Value) (jsontext.Value, error) {
		if method != "hook" || string(params) != `{"tool_use_id":"native"}` {
			t.Errorf("callback %s %s", method, params)
		}
		close(entered)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	c, err := start(ctx, cfg, []byte(transportFixture))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Request(ctx, "hook", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := c.Request(ctx, "cancel", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	requestCtx, stop := context.WithCancel(ctx)
	stop()
	if _, err := c.Request(requestCtx, "block", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request = %v", err)
	}
	requestCtx, stop = context.WithTimeout(ctx, 10*time.Millisecond)
	defer stop()
	if _, err := c.Request(requestCtx, "block", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncertain request = %v", err)
	}
	if _, err := c.Request(ctx, "echo", jsontext.Value(`"still usable"`)); err != nil {
		t.Fatal(err)
	}
}

func TestClientWriteCancellationAndCloseUnresponsiveWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// The worker initializes, then pauses its input. A large request fills the
	// pipe; caller cancellation and runtime shutdown must still make progress.
	worker := strings.Replace(transportFixture, "result:{hooks_applied:true}}); return;", "result:{hooks_applied:true}}); process.stdin.pause(); lines.pause(); setInterval(()=>{},10000); return;", 1)
	c, err := start(ctx, fixtureConfig(t), []byte(worker))
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	params, err := json.Marshal(strings.Repeat("x", 1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request(requestCtx, "block", params); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write = %v", err)
	}
	c.cancel()
	_ = c.Close()
	if c.cmd.ProcessState == nil {
		t.Fatal("unresponsive worker was not joined")
	}
}

func TestClientBundledSDK(t *testing.T) {
	if _, err := bundle.ReadFile("dist/worker.mjs"); err != nil {
		t.Skip("optional Claude SDK bundle has not been built")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cfg := fixtureConfig(t)
	path, err := filepath.Abs("testdata/mock-claude.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClaudePath = path
	c, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	params := jsontext.Value(`{"type":"user","uuid":"f3290761-830b-4720-9048-c9923e597c40","parent_tool_use_id":null,"shouldQuery":false,"message":{"role":"user","content":"retained"}}`)
	models, err := c.Models()
	if err != nil || len(models) != 1 || models[0].Value != "fixture-model" || models[0].DisplayName != "Fixture Model" || models[0].ResolvedModel != "fixture-resolved" {
		t.Fatalf("SDK initialization models = %+v, %v", models, err)
	}
	ack, err := c.Request(ctx, "input", params)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ack), `"status":"queued"`) {
		t.Fatalf("input acknowledgement = %s", ack)
	}
	for {
		select {
		case raw, open := <-c.Events():
			if !open {
				t.Fatalf("SDK ended: %v; %s", c.Err(), c.Diagnostics())
			}
			var event Event
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "result" && (event.NumTurns == nil || *event.NumTurns != 0) {
				t.Fatal("retained-only input started inference")
			}
			if event.CommandUUID == "f3290761-830b-4720-9048-c9923e597c40" && event.State == "completed" {
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("SDK input replay: %v; %s", ctx.Err(), c.Diagnostics())
		}
	}
}

func TestClientUnexpectedCleanExitIsTransportLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	worker := strings.Replace(transportFixture, "v.method==='shutdown'", "v.method==='shutdown'||v.method==='exit'", 1)
	c, err := start(ctx, fixtureConfig(t), []byte(worker))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, _ = c.Request(ctx, "exit", nil)
	select {
	case <-c.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if c.Err() == nil || !strings.Contains(c.Err().Error(), "closed unexpectedly") {
		t.Fatalf("lost transport outcome = %v", c.Err())
	}
}

func TestClientBundledSDKStartupFailure(t *testing.T) {
	if _, err := bundle.ReadFile("dist/worker.mjs"); err != nil {
		t.Skip("optional Claude SDK bundle has not been built")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cfg := fixtureConfig(t)
	path, err := filepath.Abs("testdata/mock-claude.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClaudePath = path
	cfg.Env = append(cfg.Env, "MEKUGI_FIXTURE_INIT_ERROR=1")
	c, err := Start(ctx, cfg)
	if c != nil {
		_ = c.Close()
		t.Fatal("startup failure admitted a client")
	}
	if err == nil || !strings.Contains(err.Error(), "exited with code 23") {
		t.Fatalf("startup error lost native diagnostics: %v", err)
	}
}
