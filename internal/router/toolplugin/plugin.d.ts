/// <reference path="./core-v1.d.ts" />

export type CustomCarrier = {
  kind: "custom";
  name: string;
  payload: string;
};

export type FunctionCarrier = {
  kind: "function";
  name: string;
  payload: string;
};

export type ExecCarrier = {
  kind: "exec";
  template?: string;
  params?: Record<string, unknown>;
  retainInput?: boolean;
};

export type Carrier = CustomCarrier | FunctionCarrier | ExecCarrier;

export type TranslationAPI = {
  custom(name: string, input: string): CustomCarrier;
  function(name: string, argumentsJSON: string): FunctionCarrier;
  exec(
    template?: string,
    params?: Record<string, unknown>,
	retainInput?: boolean,
  ): ExecCarrier;
};

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

export type TranslationContext = {
  resolvePath(path: string): string;
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
  parse(input: string, context: TranslationContext): T | Promise<T>;
  argv(input: T, context: TranslationContext): string[] | Promise<string[]>;
  translate(input: T, api: TranslationAPI, context: TranslationContext): Carrier | Promise<Carrier>;
  execute(argv: string[], context: ExecutionContext): ExecutionResult | Promise<ExecutionResult>;
};

export type Plugin = {
  apiVersion: "mekugi-tool-plugin/v1";
  id: string;
  tools: Tool<unknown>[];
};
