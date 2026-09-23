import {readFileSync} from "node:fs";
import {WASI} from "node:wasi";

const ABI_VERSION = 1;
const operations = Object.freeze({
  parsePositiveInteger: 2,
  decodeQuotedOperand: 3,
  classifySourcePath: 4,
  isGoIdentifier: 5,
  decodeGoStringLiteral: 6,
  parseShellHeader: 7,
  interpreterIdentity: 8,
});

const wasi = new WASI({version: "preview1", args: [], env: {}, preopens: {}});
const wasmBytes = readFileSync(new URL("./shared_core.wasm", import.meta.url));
// Bun's Node-compatible WASI surface used by the owning tests predates
// getImportObject; both forms provide the same preview1 import namespace.
const importObject = typeof wasi.getImportObject === "function"
  ? wasi.getImportObject()
  : {wasi_snapshot_preview1: wasi.wasiImport};
const module = new WebAssembly.Module(wasmBytes);
const instance = new WebAssembly.Instance(module, importObject);
if (typeof wasi.initialize === "function") {
  wasi.initialize(instance);
} else {
  wasi.setMemory(instance.exports.memory);
  instance.exports._initialize();
}

const wasm = instance.exports;
for (const name of [
  "memory",
  "mekugi_core_abi_version",
  "mekugi_core_reserve_input",
  "mekugi_core_line_count",
  "mekugi_core_line_bounds",
  "mekugi_core_invoke",
  "mekugi_core_result_pointer",
]) {
  if (!(name in wasm)) {
    throw new Error(`shared-core WASM export ${name} is unavailable`);
  }
}
if (wasm.mekugi_core_abi_version() !== ABI_VERSION) {
  throw new Error(`shared-core WASM ABI must be version ${ABI_VERSION}`);
}

const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", {fatal: true});

export class SharedCoreError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "SharedCoreError";
    this.code = code;
  }
}

function inputBytes(value) {
  if (typeof value === "string") {
    return encoder.encode(value);
  }
  if (value instanceof Uint8Array) {
    return value;
  }
  throw new TypeError("shared-core input must be a string or Uint8Array");
}

function loadInput(value) {
  const bytes = inputBytes(value);
  if (bytes.byteLength > 0xffff_ffff) {
    throw new SharedCoreError("input_too_large", "shared-core input exceeds WASM32 memory");
  }
  const pointer = wasm.mekugi_core_reserve_input(bytes.byteLength);
  if (bytes.byteLength !== 0) {
    new Uint8Array(wasm.memory.buffer, pointer, bytes.byteLength).set(bytes);
  }
}

function invoke(operation, value) {
  loadInput(value);
  const length = wasm.mekugi_core_invoke(operation);
  const pointer = wasm.mekugi_core_result_pointer();
  const encoded = new Uint8Array(wasm.memory.buffer, pointer, length);
  const response = JSON.parse(decoder.decode(encoded));
  if (response?.ok !== true) {
    const error = response?.error;
    throw new SharedCoreError(
      typeof error?.code === "string" ? error.code : "invalid_result",
      typeof error?.message === "string" ? error.message : "shared-core returned an invalid result",
    );
  }
  return response.value ?? null;
}

export function lineCount(value) {
  loadInput(value);
  return wasm.mekugi_core_line_count();
}

export function lineBounds(value, line) {
  if (!Number.isSafeInteger(line) || line < 1 || line > 0xffff_ffff) {
    return null;
  }
  loadInput(value);
  const pointer = wasm.mekugi_core_line_bounds(line);
  if (pointer === 0) {
    return null;
  }
  const view = new DataView(wasm.memory.buffer, pointer, 12);
  return Object.freeze({
    byteStart: view.getUint32(0, true),
    byteContentEnd: view.getUint32(4, true),
    byteEnd: view.getUint32(8, true),
  });
}

export function parsePositiveInteger(value) {
  return invoke(operations.parsePositiveInteger, value);
}

export function decodeQuotedOperand(value) {
  return invoke(operations.decodeQuotedOperand, value);
}

export function classifySourcePath(value) {
  return invoke(operations.classifySourcePath, value);
}

export function isGoIdentifier(value) {
  return invoke(operations.isGoIdentifier, value);
}

export function decodeGoStringLiteral(value) {
  return invoke(operations.decodeGoStringLiteral, value);
}

export function parseShellHeader(value) {
  const parsed = invoke(operations.parseShellHeader, value);
  if (parsed.hasParams !== true) {
    delete parsed.params;
  }
  delete parsed.hasParams;
  return parsed;
}

export function interpreterIdentity(value) {
  return invoke(operations.interpreterIdentity, value);
}
