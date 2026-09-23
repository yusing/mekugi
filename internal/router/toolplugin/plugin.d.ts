/// <reference path="./core-v1.d.ts" />

export type ExecutionOutput = {
  stdout?: string;
  stderr?: string;
  exitCode: number;
};

export type ReaderFailureClass = "invalid_arguments" | "not_found" | "permission_denied" |
  "not_regular" | "invalid_source" | "reader_error" | "search_error" | "resolver_error" |
  "dependency_unavailable" | "no_editable_location" | "output_limit";

export type ExecutionResult = ExecutionOutput & {
  // Omitted suffixes for host-owned managed recovery, never a temporary-file path.
  omittedOutput?: {stdout: string; stderr: string; stdoutKind?: "rows" | "json"; stderrKind?: "rows" | "json"};
  // Allowlisted diagnostic metadata, never command output or a raw error.
  failureClass?: ReaderFailureClass;
  // Private host cleanup metadata, never part of the executor-facing output.
  terminationReason?: "output_limit" | "resolver_cleanup";
};

export type ExecutionContext = {
  stdinFD: number | null;
  scriptReadFD: number | null;
  scriptWriteFD: number | null;
  outputBudgetBytes: number;
};

export type Tool<T> = {
  specification: {
    type: "custom";
    name: string;
    description: string;
    format?: {
      type: "grammar";
      syntax: "lark" | "regex";
      definition: string;
    };
  };
  parse(input: string): T | Promise<T>;
  argv(input: T): string[] | Promise<string[]>;
  execute(argv: string[], context: ExecutionContext): ExecutionResult | Promise<ExecutionResult>;
};

// Bundled tools with router-owned state or process execution use a pinned
// native backend while retaining their plugin-owned specification.
export type NativeTool = {
  specification: Tool<unknown>["specification"];
  nativeExecutor: "mread" | "mrun" | "mchanges";
};

export type Plugin = {
  apiVersion: "mekugi-tool-plugin/v1";
  id: string;
  tools: (Tool<unknown> | NativeTool)[];
};
