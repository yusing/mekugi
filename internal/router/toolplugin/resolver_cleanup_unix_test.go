//go:build unix

package toolplugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolverCleanupRetiresInheritedPipeDescendants(t *testing.T) {
	t.Parallel()
	snapshot, err := Load(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"gopls", "lsp"} {
		outcomes := []string{"success", "failure", "timeout"}
		if kind == "lsp" {
			outcomes = append(outcomes, "ignore_shutdown", "exit", "queued_success", "queued_no_response")
		}
		for _, outcome := range outcomes {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				directory, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				pidPath := filepath.Join(directory, "descendant.pid")
				t.Cleanup(func() {
					encoded, _ := os.ReadFile(pidPath)
					if pid, err := strconv.Atoi(string(encoded)); err == nil {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				})
				name, line, source, row, executable := "input.go", "var Target = 1", "package p\nvar Target = 1\n", "2", "gopls"
				if kind == "lsp" {
					name, line, source, row, executable = "input.ts", "const Target = 1;", "const Target = 1;\n", "1", "tsc"
				}
				inputPath := filepath.Join(directory, name)
				if err := os.WriteFile(inputPath, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				fixture := filepath.Join(directory, "resolver.mjs")
				if err := os.WriteFile(fixture, []byte(resolverCleanupFixture), 0600); err != nil {
					t.Fatal(err)
				}
				quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
				wrapper := "#!/bin/sh\nexec " + quote(snapshot.NodeExecutable) + " " + quote(fixture) + "\n"
				if err := os.WriteFile(filepath.Join(directory, executable), []byte(wrapper), 0700); err != nil {
					t.Fatal(err)
				}
				budget := 5 * time.Second
				cleanupDurationPath := filepath.Join(directory, "cleanup-duration")
				timerPreload := filepath.Join(directory, "timers.cjs")
				if err := os.WriteFile(timerPreload, []byte(controlledHostTimersPreload), 0600); err != nil {
					t.Fatal(err)
				}
				var deadlineDurationPath, deadlineReadyPath, deadlineExpiredPath string
				if outcome == "timeout" {
					deadlineDurationPath = filepath.Join(directory, "deadline-duration")
					deadlineReadyPath = filepath.Join(directory, "deadline-ready")
					deadlineExpiredPath = filepath.Join(directory, "deadline-expired")
				}
				ctx, cancel := context.WithTimeout(t.Context(), budget)
				defer cancel()
				nodeOptions := strings.TrimSpace(os.Getenv("NODE_OPTIONS") + " --require=" + strconv.Quote(timerPreload))
				accelerateCleanup := ""
				if kind == "gopls" {
					accelerateCleanup = "1"
				}
				environment := append(os.Environ(), "PATH="+directory, "FIXTURE_KIND="+kind, "FIXTURE_OUTCOME="+outcome,
					"FIXTURE_PID="+pidPath, "FIXTURE_SOURCE="+inputPath, "FIXTURE_HOST="+filepath.Join(snapshot.Root, hostFilename),
					"FIXTURE_CLEANUP_DURATION="+cleanupDurationPath, "FIXTURE_ACCELERATE_CLEANUP="+accelerateCleanup,
					"NODE_OPTIONS="+nodeOptions)
				if outcome == "timeout" {
					environment = append(environment, "FIXTURE_DEADLINE_DURATION="+deadlineDurationPath,
						"FIXTURE_DEADLINE_READY="+deadlineReadyPath, "FIXTURE_DEADLINE_EXPIRED="+deadlineExpiredPath)
				}
				started := time.Now()
				result, err := Execute(ctx, snapshot.NodeExecutable, snapshot.Root, "builtin/tools.js", 1,
					[]string{"refs", name, row, "Target"}, nil, directory, environment)
				if err != nil {
					t.Fatal(err)
				}
				if time.Since(started) >= budget-time.Second {
					t.Fatal("resolver exceeded its cleanup bound")
				}
				if outcome == "success" || outcome == "ignore_shutdown" || outcome == "queued_success" {
					expected := strconv.Quote(name) + ":" + row + " " + line + "\n"
					diagnostic := "msymbol: input " + strconv.Quote(name) + ":" + row + " (current snapshot)\n"
					if result.ExitCode != 0 || result.Stdout != expected || result.Stderr != diagnostic {
						t.Fatalf("semantic result changed during cleanup: %+v", result)
					}
				} else {
					if result.ExitCode != 1 || result.Stdout != "" || result.Stderr == "" {
						t.Fatalf("resolver failure = %+v", result)
					}
					if outcome == "timeout" && !strings.Contains(result.Stderr, "deadline exceeded") {
						t.Fatalf("timeout result = %+v", result)
					}
				}
				if encoded, err := os.ReadFile(cleanupDurationPath); err != nil || string(encoded) != "1000" {
					t.Fatalf("controlled resolver cleanup duration = %q, %v; want %q", encoded, err, "1000")
				}
				if outcome == "timeout" {
					for path, want := range map[string]string{
						deadlineDurationPath: "30000",
						deadlineReadyPath:    "ready",
						deadlineExpiredPath:  "ready",
					} {
						encoded, err := os.ReadFile(path)
						if err != nil || string(encoded) != want {
							t.Fatalf("controlled resolver deadline evidence %s = %q, %v; want %q", filepath.Base(path), encoded, err, want)
						}
					}
				}
				encoded, err := os.ReadFile(pidPath)
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(encoded))
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("resolver descendant %d survived cleanup", pid)
			})
		}
	}
}

const resolverCleanupFixture = `import {spawn} from "node:child_process";
import {writeFileSync} from "node:fs";
import {pathToFileURL} from "node:url";
const outcome = process.env.FIXTURE_OUTCOME;
const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
  stdio: outcome.startsWith("queued_") ? "ignore" : "inherit",
});
writeFileSync(process.env.FIXTURE_PID, String(child.pid));
if (process.env.FIXTURE_KIND === "gopls") {
  if (outcome === "success") process.stdout.write(process.env.FIXTURE_SOURCE + ":2:5-11\n", () => process.exit(0));
  else if (outcome === "failure") process.stderr.write("query failed\n", () => process.exit(1));
  else if (outcome === "timeout") writeFileSync(process.env.FIXTURE_DEADLINE_READY, "ready");
} else {
  let input = Buffer.alloc(0);
  function frame(message) {
    const body = JSON.stringify(message);
    return "Content-Length: " + Buffer.byteLength(body) + "\r\n\r\n" + body;
  }
  function respond(id, result) {
    process.stdout.write(frame({jsonrpc:"2.0", id, result}));
  }
  process.stdin.on("data", (chunk) => {
    input = Buffer.concat([input, chunk]);
    while (true) {
      const end = input.indexOf("\r\n\r\n");
      if (end < 0) return;
      const length = Number(/Content-Length: ([0-9]+)/iu.exec(input.subarray(0,end).toString())?.[1]);
      if (input.length < end+4+length) return;
      const message = JSON.parse(input.subarray(end+4,end+4+length).toString());
      input = input.subarray(end+4+length);
      if (message.method === "initialize") {
        if (outcome === "exit") process.exit(0);
        respond(message.id, {capabilities:{positionEncoding:outcome === "failure" ? "utf-8" : "utf-16"}});
      } else if (message.method === "textDocument/references" && outcome === "timeout") {
        writeFileSync(process.env.FIXTURE_DEADLINE_READY, "ready");
      } else if (message.method === "textDocument/references") {
        const result = [{uri:pathToFileURL(process.env.FIXTURE_SOURCE).href, range:{start:{line:0,character:6},end:{line:0,character:12}}}];
        if (outcome.startsWith("queued_")) {
          // The real JSON-RPC adapter dispatches these notifications one per
          // setImmediate. Close the pipes before the response reaches dispatch.
          const messages = Array.from({length:10_000}, (_, index) => frame({
            jsonrpc:"2.0", method:"window/logMessage", params:{type:3, message:String(index)},
          }));
          if (outcome === "queued_success") messages.push(frame({jsonrpc:"2.0", id:message.id, result}));
          process.stdout.write(messages.join(""), () => process.exit(0));
        } else respond(message.id, result);
      } else if (message.method === "shutdown" && outcome !== "ignore_shutdown") respond(message.id, null);
      else if (message.method === "exit") process.exit(0);
    }
  });
}
setInterval(() => {}, 1000);
`

// The resolver cases exercise real subprocess and process-group cleanup without
// sleeping for its full grace period. The preload records the shipped timer
// durations and advances only those timers in the plugin host. The timeout timer
// is released only after the semantic query starts.
const controlledHostTimersPreload = `const {existsSync, readFileSync, writeFileSync} = require("node:fs");
const realSetTimeout = globalThis.setTimeout;
const realClearTimeout = globalThis.clearTimeout;
const controlled = new Map();
globalThis.setTimeout = function(callback, delay, ...args) {
  if (delay === 1_000 && process.argv[1] === process.env.FIXTURE_HOST) {
    writeFileSync(process.env.FIXTURE_CLEANUP_DURATION, String(delay));
    return realSetTimeout(callback, process.env.FIXTURE_ACCELERATE_CLEANUP ? 1 : delay, ...args);
  }
  if (delay !== 30_000 || !process.env.FIXTURE_DEADLINE_READY) {
    return realSetTimeout(callback, delay, ...args);
  }
  writeFileSync(process.env.FIXTURE_DEADLINE_DURATION, String(delay));
  const token = {};
  const state = {cancelled: false, handle: undefined};
  controlled.set(token, state);
  const poll = () => {
    if (state.cancelled) return;
    if (existsSync(process.env.FIXTURE_DEADLINE_READY)
        && readFileSync(process.env.FIXTURE_DEADLINE_READY, "utf8") === "ready") {
      writeFileSync(process.env.FIXTURE_DEADLINE_EXPIRED, "ready");
      callback(...args);
      return;
    }
    state.handle = realSetTimeout(poll, 1);
  };
  state.handle = realSetTimeout(poll, 1);
  return token;
};
globalThis.clearTimeout = function(timer) {
  const state = controlled.get(timer);
  if (state === undefined) return realClearTimeout(timer);
  controlled.delete(timer);
  state.cancelled = true;
  return realClearTimeout(state.handle);
};
`
