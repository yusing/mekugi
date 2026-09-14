import {spawn} from "node:child_process";
import {closeSync} from "node:fs";
import {Socket} from "node:net";
import {
  interpreterIdentity,
  parseShellHeader,
} from "mekugi:core/v1";

/**
 * parseScript parses a shell script using the shared-core shell header parser.
 */
function parseScript(input) {
  const parsed = parseShellHeader(input);
  if (Object.hasOwn(parsed, "scriptPath")) {
    throw new Error("retained shell references must be resolved by the router");
  }
  if (parsed.params !== undefined) {
    if (Object.hasOwn(parsed.params, "cmd")) {
      throw new Error(`line ${parsed.paramsLine}: #!params must not contain cmd; the script body supplies it`);
    }
    if (Object.hasOwn(parsed.params, "login") && parsed.params.login !== false) {
      throw new Error(`line ${parsed.paramsLine}: #!params login must be false`);
    }
  }
  return {
    interpreter: parsed.interpreter ?? ["bash"],
    body: parsed.body ?? "",
    commandTemplate: parsed.commandTemplate ?? "",
    source: input,
    params: parsed.params,
  };
}

/**
 * retainInput determines whether the script source should be retained for translation.
 */
function retainInput(input) {
  const interpreter = interpreterIdentity(input.interpreter[0]);
  if (interpreter !== "bash" && interpreter !== "sh") {
    return true;
  }
  const normalized = input.source.replaceAll("\r\n", "\n");
  return normalized.split(/\n|\r/u).length > 3;
}

/**
 * executionError extracts a string message from an error value.
 */
function executionError(error) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * scriptEvaluationFlag returns the command-line flag for inline script evaluation.
 */
function scriptEvaluationFlag(interpreter) {
  switch (interpreterIdentity(interpreter)) {
    case "bun":
    case "node":
    case "nodejs":
      return "-e";
    default:
      return null;
  }
}


/**
 * executeInterpreter runs a script with bounded output and inherited-pipe cleanup.
 */
function executeInterpreter(argv, context) {
  const interpreter = argv[0];
  const body = argv.at(-1);
  const evaluationFlag = scriptEvaluationFlag(interpreter);
  const hasProgramInput = [context?.stdinFD, context?.scriptReadFD, context?.scriptWriteFD].every(
    (fileDescriptor) => Number.isSafeInteger(fileDescriptor) && fileDescriptor >= 3,
  );
  const usesDescriptor = hasProgramInput && evaluationFlag === null;
  const interpreterArguments = !hasProgramInput ? argv.slice(1, -1)
    : usesDescriptor ? [...argv.slice(1, -1), "/dev/fd/3"]
    : [...argv.slice(1, -1), evaluationFlag, body];

  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(interpreter, interpreterArguments, {
        env: process.env,
        stdio: !hasProgramInput ? ["pipe", "pipe", "pipe"] : usesDescriptor
          ? [context.stdinFD, "pipe", "pipe", context.scriptReadFD]
          : [context.stdinFD, "pipe", "pipe"],
      });
    } catch (error) {
      resolve({stderr: `shell: ${executionError(error)}\n`, exitCode: 1});
      return;
    }

    const overflowDiagnostic = `shell: interpreter output exceeds ${context.outputBudgetBytes} bytes\n`;
    const captureBudgetBytes = Math.max(
      0,
      context.outputBudgetBytes - Buffer.byteLength(overflowDiagnostic, "utf8") - 3,
    );
    const stdoutChunks = [];
    const stderrChunks = [];
    let capturedBytes = 0;
    let overflow = false;
    let spawnError;
    let scriptError;
    let drainDeadline;
    let scriptInput;
    const truncatedStreams = new Set();

    const capture = (chunks, chunk) => {
      const bytes = Buffer.from(chunk);
      const remaining = Math.max(0, captureBudgetBytes - capturedBytes);
      if (remaining > 0) {
        chunks.push(bytes.subarray(0, remaining));
      }
      if (bytes.length > remaining) {
        truncatedStreams.add(chunks);
      }
      if (bytes.length > remaining && !overflow) {
        overflow = true;
        scriptInput?.destroy();
        child.kill("SIGKILL");
        // Inherited pipes can outlive the interpreter. Return the bounded result
        // so the host's process-group owner can retire the remaining descendants.
        drainDeadline = setTimeout(() => {
          child.stdout.destroy();
          child.stderr.destroy();
        }, 1_000);
      }
      capturedBytes += Math.min(bytes.length, remaining);
    };
    child.stdout.on("data", (chunk) => {
      capture(stdoutChunks, chunk);
    });
    child.stderr.on("data", (chunk) => {
      capture(stderrChunks, chunk);
    });
    child.on("error", (error) => {
      spawnError = error;
    });
    child.on("close", (status, signal) => {
      clearTimeout(drainDeadline);
      scriptInput?.destroy();
      const finish = (output) => resolve(overflow
        ? {...output, terminationReason: "output_limit"}
        : output);
      let stdout;
      let stderr;
      try {
        // A byte-limited prefix may end inside one otherwise valid UTF-8 rune.
        // Streaming decode omits only that incomplete suffix and still rejects
        // malformed sequences in the retained bytes.
        stdout = new TextDecoder("utf-8", {fatal: true}).decode(Buffer.concat(stdoutChunks), {stream: truncatedStreams.has(stdoutChunks)});
        stderr = new TextDecoder("utf-8", {fatal: true}).decode(Buffer.concat(stderrChunks), {stream: truncatedStreams.has(stderrChunks)});
      } catch {
        finish({stderr: "shell: interpreter output is not UTF-8\n" + (overflow ? overflowDiagnostic : ""), exitCode: 1});
        return;
      }
      if (spawnError !== undefined) {
        stderr += `shell: ${executionError(spawnError)}\n`;
        finish({
          stdout,
          stderr,
          exitCode: spawnError.code === "ENOENT" ? 127 : 1,
        });
        return;
      }
      if (overflow) {
        stderr += overflowDiagnostic;
        finish({stdout, stderr, exitCode: 1});
        return;
      }
      if (scriptError !== undefined) {
        stderr += `shell: write script body: ${executionError(scriptError)}\n`;
        finish({stdout, stderr, exitCode: 1});
        return;
      }
      if (signal !== null) {
        stderr += `shell: interpreter terminated by ${signal}\n`;
        finish({stdout, stderr, exitCode: 1});
        return;
      }
      finish({stdout, stderr, exitCode: status ?? 1});
    });

    try {
      // Wrap the inherited pipe in event-loop I/O, not a filesystem write that
      // can block while a streaming interpreter fills its output pipe.
      scriptInput = !hasProgramInput ? child.stdin
        : usesDescriptor ? new Socket({fd: context.scriptWriteFD, readable: false, writable: true})
        : null;
      if (scriptInput === null) {
        closeSync(context.scriptWriteFD);
        return;
      }
      scriptInput.on("error", (error) => {
        scriptError = error;
        child.kill("SIGKILL");
      });
      scriptInput.end(body);
    } catch (error) {
      scriptError = error;
      child.kill("SIGKILL");
      if (scriptInput !== undefined) {
        scriptInput?.destroy();
      } else if (hasProgramInput) {
        // Ownership transfers to Socket only after construction succeeds.
        try { closeSync(context.scriptWriteFD); } catch {}
      }
    }
  });
}

/**
 * executeScript executes a shell script with the appropriate interpreter.
 */
function executeScript(argv, context) {
  if (argv.length < 2) {
    return {stderr: "shell: missing interpreter or script body\n", exitCode: 1};
  }
  const interpreter = interpreterIdentity(argv[0]);
  // Bash and POSIX shell programs must pass through the router's mvdan/sh
  // runner so private commands cannot fall back to executable frontends.
  if (interpreter === "bash" || interpreter === "sh") {
    return {stderr: "shell: bash and sh require the router shell runner\n", exitCode: 1};
  }
  const parsed = parseShellHeader(argv.at(-1));
  return executeInterpreter([...argv.slice(0, -1), parsed.body ?? ""], context);
}

export const shellTool = {
  specification: {
    type: "custom",
    name: "shell",
    description: `Run free-form scripts with the exact body passed to the selected interpreter; standard input remains program data.`,
  },

  parse(input, context) {
    return parseScript(input, context);
  },

  argv(input) {
    return [...input.interpreter, input.source];
  },

  translate(input, api) {
    const template = input.commandTemplate === "" ? undefined : input.commandTemplate;
    return api.exec(template, input.params, retainInput(input));
  },

  execute(argv, context) {
    return executeScript(argv, context);
  },
};

export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "builtin.shell",
  tools: [shellTool],
};
