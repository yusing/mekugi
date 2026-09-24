import {resolverProcess, ResolverTimeout, withResolverDeadline} from "./resolver.ts";
import {spawn} from "node:child_process";
import {pathToFileURL} from "node:url";

import {createMessageConnection} from "vscode-jsonrpc/node";

import {collect, decodeUTF8, errorText} from "./common.ts";

type LSPPosition = {
  line: number;
  character: number;
};

type LSPRange = {
  start: LSPPosition;
  end: LSPPosition;
};

export type LSPLocation = {
  uri: string;
  range: LSPRange;
};

type LSPQueryResult = {
  locations: LSPLocation[];
  stderr: string;
};

type LSPQueryOptions = {
  command: string;
  args: string[];
  workspace: string;
  path: string;
  languageID: string;
  source: string;
  position: LSPPosition;
  mode: "def" | "refs";
};

function position(value: unknown): LSPPosition | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  const {line, character} = value as {line?: unknown; character?: unknown};
  return Number.isSafeInteger(line) && (line as number) >= 0
    && Number.isSafeInteger(character) && (character as number) >= 0
    ? {line: line as number, character: character as number}
    : null;
}

function range(value: unknown): LSPRange | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  const start = position((value as {start?: unknown}).start);
  const end = position((value as {end?: unknown}).end);
  return start === null || end === null ? null : {start, end};
}

function location(value: unknown): LSPLocation | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  const candidate = value as {
    uri?: unknown;
    range?: unknown;
    targetUri?: unknown;
    targetRange?: unknown;
    targetSelectionRange?: unknown;
  };
  const uri = typeof candidate.uri === "string"
    ? candidate.uri
    : typeof candidate.targetUri === "string" ? candidate.targetUri : null;
  const selectedRange = candidate.targetSelectionRange ?? candidate.range ?? candidate.targetRange;
  const parsedRange = range(selectedRange);
  return uri === null || parsedRange === null ? null : {uri, range: parsedRange};
}

function locations(value: unknown, mode: "def" | "refs"): LSPLocation[] {
  if (value === null) {
    return [];
  }
  const values = Array.isArray(value) ? value : mode === "def" ? [value] : null;
  if (values === null) {
    throw new Error("invalid references response");
  }
  return values.map((value) => {
    const parsed = location(value);
    if (parsed === null) {
      throw new Error(`invalid ${mode === "def" ? "definition" : "references"} response`);
    }
    return parsed;
  });
}

function semanticStderr(stderr: string): string {
  return stderr
    .split(/(?<=\n)/u)
    .filter((line) => {
      const message = line.trim();
      return message !== "context canceled" && message !== "error handling method 'exit': EOF";
    })
    .join("");
}

function processFailure(command: string, error: Error): Error {
  return "code" in error && error.code === "ENOENT"
    ? new Error(`${command} is unavailable`)
    : new Error(`cannot start ${command}: ${errorText(error)}`);
}

export async function runLSPQuery(options: LSPQueryOptions): Promise<LSPQueryResult> {
  const result = await runLSPQueries(options, [options]);
  return {locations: result.locations[0], stderr: result.stderr};
}

export async function runLSPQueries(
  options: Pick<LSPQueryOptions, "command" | "args" | "workspace">,
  queries: Omit<LSPQueryOptions, "command" | "args" | "workspace">[],
): Promise<{locations: LSPLocation[][]; stderr: string}> {
  return withResolverDeadline(async (deadline) => {
    const child = spawn(options.command, options.args, {
      cwd: options.workspace,
      stdio: ["pipe", "pipe", "pipe"],
    });
    const started = new Promise<Error | null>((resolve) => {
      child.once("spawn", () => resolve(null));
      child.once("error", (error) => resolve(error));
    });
    const processLifecycle = resolverProcess(child);
    const stderrPromise = collect(child.stderr);
    const startError = await started;
    if (startError !== null) {
      await processLifecycle.finish();
      await stderrPromise;
      throw processFailure(options.command, startError);
    }
    const connection = createMessageConnection(child.stdout, child.stdin, {
      error() {},
      warn() {},
      info() {},
      log() {},
    });
    const workspaceURI = pathToFileURL(options.workspace).href;
    const workspaceName = options.workspace.split(/[\\/]/u).at(-1) ?? options.workspace;
    connection.onRequest("workspace/configuration", (params: unknown) => {
      const items = params !== null && typeof params === "object" && !Array.isArray(params)
        ? (params as {items?: unknown}).items
        : null;
      return Array.isArray(items) ? items.map(() => null) : [];
    });
    connection.onRequest("workspace/workspaceFolders", () => [
      {uri: workspaceURI, name: workspaceName},
    ]);
    connection.onRequest("client/registerCapability", () => null);
    connection.onRequest("window/workDoneProgress/create", () => null);
    connection.listen();

    let protocolFinished = false;
    let protocolDrainTimer: ReturnType<typeof setTimeout> | undefined;
    const processEnded = new Promise<never>((_, reject) => {
      void processLifecycle.exited.then(() => {
        if (protocolFinished) return;
        // Pipe EOF does not mean the JSON-RPC dispatch queue is empty. Keep an
        // independent grace period for a buffered reply, even when pipes close
        // immediately; a missing reply still fails within this bound.
        protocolDrainTimer = setTimeout(() => {
          reject(new Error("language server exited before completing the query"));
        }, 1_000);
      });
    });
    void processEnded.catch(() => {});
    let phase = "initialize";
    try {
      const initialized = await Promise.race([
        connection.sendRequest("initialize", {
          processId: process.pid,
          clientInfo: {name: "mekugi", version: "1"},
          rootUri: workspaceURI,
          workspaceFolders: [{uri: workspaceURI, name: workspaceName}],
          capabilities: {
            general: {positionEncodings: ["utf-16"]},
            workspace: {configuration: true, workspaceFolders: true},
            textDocument: {
              definition: {linkSupport: true},
              references: {},
              synchronization: {},
            },
          },
        }),
        deadline,
        processEnded,
      ]);
      const positionEncoding = initialized !== null && typeof initialized === "object"
        ? (initialized as {capabilities?: {positionEncoding?: unknown}}).capabilities?.positionEncoding
        : undefined;
      if (positionEncoding !== undefined && positionEncoding !== "utf-16") {
        throw new Error(`language server selected unsupported position encoding ${String(positionEncoding)}`);
      }
      phase = "open document";
      await Promise.race([connection.sendNotification("initialized", {}), deadline, processEnded]);
      const parsedLocations: LSPLocation[][] = [];
      const opened = new Set<string>();
      for (const query of queries) {
        const uri = pathToFileURL(query.path).href;
        if (!opened.has(uri)) {
          phase = "open document";
          opened.add(uri);
          await Promise.race([connection.sendNotification("textDocument/didOpen", {
            textDocument: {
              uri,
              languageId: query.languageID,
              version: 1,
              text: query.source,
            },
          }), deadline, processEnded]);
        }
        phase = query.mode === "def" ? "definition" : "references";
        const response = query.mode === "def"
          ? await Promise.race([
            connection.sendRequest("textDocument/definition", {
              textDocument: {uri},
              position: query.position,
            }),
            deadline,
            processEnded,
          ])
          : await Promise.race([
            connection.sendRequest("textDocument/references", {
              textDocument: {uri},
              position: query.position,
              context: {includeDeclaration: true},
            }),
            deadline,
            processEnded,
          ]);
        parsedLocations.push(locations(response, query.mode));
      }
      phase = "shutdown";
      // Cleanup is auxiliary once the semantic response is complete. Bound the
      // child lifetime even when a server ignores shutdown or exit.
      const completion = await processLifecycle.finish(async () => {
        const shutdownCompleted = await Promise.race([
          connection.sendRequest("shutdown").then(() => true, () => false),
          processLifecycle.exited.then(() => false),
        ]);
        if (shutdownCompleted) {
          await connection.sendNotification("exit");
          child.stdin.end();
        } else {
          child.kill("SIGKILL");
        }
      });
      const cleanupError = completion.error ?? null;
      const stderr = semanticStderr(decodeUTF8(await stderrPromise, "language server stderr"));
      return {
        locations: parsedLocations,
        stderr: cleanupError !== null && stderr === "" ? semanticStderr(`child error: ${errorText(cleanupError)}`) : stderr,
      };
    } catch (error) {
      child.kill("SIGKILL");
      const completionError = (await processLifecycle.finish()).error ?? null;
      await stderrPromise;
      if (completionError !== null && "code" in completionError && completionError.code === "ENOENT") {
        throw processFailure(options.command, completionError);
      }
      if (error instanceof ResolverTimeout) throw error;
      const message = error instanceof Error ? error.message : errorText(error);
      throw new Error(`${phase} failed: ${message}`);
    } finally {
      protocolFinished = true;
      clearTimeout(protocolDrainTimer);
      connection.dispose();
    }
  });
}
