import {countGPT5Tokens} from "./tokens.ts";
import {selectMRunText} from "./mrun.ts";
import type {NativeTool} from "../internal/router/toolplugin/plugin.d.ts";

type ReadKind = "" | "rows" | "json";
export type ReadPageRequest = {
  stdout: string; stderr: string;
  stdoutKind: ReadKind; stderrKind: ReadKind;
  position: [number, number]; stream: "" | "stdout" | "stderr";
  sourceRow?: number; label?: string;
};
export type ReadPage = {text: string; position: [number, number]; complete: boolean; neededTokens?: number};

// JSON.parse validates syntax, but its numeric values must never become the
// recovered evidence. Split the validated top-level array into original slices.
function jsonArrayEntries(value: string): string[] {
  if (!Array.isArray(JSON.parse(value))) throw new Error("structured read output must be a JSON array");
  const text = value.trim();
  if (text.slice(1, -1).trim() === "") return [];
  const entries: string[] = [];
  let start = 1, depth = 0;
  let quoted = false, escaped = false;
  for (let index = 1; index < text.length - 1; index++) {
    const char = text[index];
    if (quoted) {
      if (escaped) escaped = false;
      else if (char === "\\") escaped = true;
      else if (char === "\"") quoted = false;
      continue;
    }
    if (char === "\"") quoted = true;
    else if (char === "[" || char === "{") depth++;
    else if (char === "]" || char === "}") depth--;
    else if (char === "," && depth === 0) {
      entries.push(text.slice(start, index).trim());
      start = index + 1;
    }
  }
  entries.push(text.slice(start, -1).trim());
  return entries;
}

// One pagination owner selects bytes, whole rows, or complete JSON
// entries. The token budget includes the stream frames, not the next-call notice.
export function selectReadOutput(request: ReadPageRequest, budget: number): ReadPage {
  const values = [request.stdout, request.stderr];
  const kinds = [request.stdoutKind, request.stderrKind];
  const position: [number, number] = [...request.position];
  const pages = ["", ""];
  const sourceStart = request.sourceRow
    ? request.sourceRow + (Buffer.from(request.stdout).subarray(0, request.position[0]).toString("utf8").match(/\n/gu)?.length ?? 0)
    : 0;
  const entries = values.map((value, index): string[] | null => kinds[index] === "json" ? jsonArrayEntries(value) : null);
  const included = (index: number): boolean => request.stream === "" || request.stream === ["stdout", "stderr"][index];
  const size = (index: number): number => entries[index]?.length ?? Buffer.byteLength(values[index]);
  const frame = (): string => {
    let prefix = request.label ? `--- ${request.label} ---\n` : "";
    if (pages[0] && sourceStart) {
      const end = sourceStart + (pages[0].match(/\n/gu)?.length ?? 0) - 1;
      prefix += `[rows ${sourceStart}:${end}]\n`;
    }
    if (!pages[1]) return pages[0] ? prefix + pages[0] : "";
    return prefix + pages.map((page, index) => {
      if (!page) return "";
      const name = ["stdout", "stderr"][index];
      return `[${name} ${kinds[index] || "bytes"}]\n${page}\n[/${name}]\n`;
    }).join("");
  };
  const fits = (): boolean => countGPT5Tokens(frame()) <= budget;
  for (let index = 0; index < 2; index++) {
    const offset = position[index];
    if (!Number.isSafeInteger(offset) || offset < 0 || offset > size(index)) throw new Error("invalid read position");
    if (!included(index)) continue;
    if (entries[index]?.length === 0) pages[index] = "[]";
  }
  if (!fits()) return {text: "", position: request.position, complete: false, neededTokens: countGPT5Tokens(frame())};
  let advanced = false;
  for (let index = 0; index < 2; index++) {
    if (!included(index) || position[index] === size(index)) continue;
    const offset = position[index];
    const array = entries[index];
    if (array !== null) {
      let low = 0;
      let high = 0;
      let bytes = 2;
      while (offset + high < array.length && bytes <= budget * 128) {
        bytes += Buffer.byteLength(array[offset + high]) + 1;
        high++;
      }
      while (low < high) {
        const middle = Math.ceil((low + high) / 2);
        pages[index] = "[" + array.slice(offset, offset + middle).join(",") + "]";
        if (fits()) low = middle;
        else high = middle - 1;
      }
      pages[index] = low > 0 ? "[" + array.slice(offset, offset + low).join(",") + "]" : "";
      position[index] += low;
    } else {
      const bytes = Buffer.from(values[index]);
      if (offset < bytes.length && (bytes[offset] & 0xc0) === 0x80) throw new Error("read position splits UTF-8");
      if (kinds[index] === "rows" && offset > 0 && bytes[offset - 1] !== 10) throw new Error("read position splits a complete row");
      let end = Math.min(bytes.length, offset + budget * 128 + 4);
      while (end < bytes.length && (bytes[end] & 0xc0) === 0x80) end--;
      const remaining = bytes.subarray(offset, end).toString("utf8");
      let allowance = budget;
      while (allowance > 0) {
        let selected = selectMRunText(remaining, allowance, false).text;
        if (kinds[index] === "rows" && (selected.length < remaining.length || end < bytes.length)) {
          selected = selected.slice(0, selected.lastIndexOf("\n") + 1);
        }
        pages[index] = selected;
        const excess = countGPT5Tokens(frame()) - budget;
        if (excess <= 0) break;
        allowance -= Math.max(1, excess);
      }
      if (allowance <= 0) pages[index] = "";
      position[index] += Buffer.byteLength(pages[index]);
    }
    if (position[index] === offset) {
      if (!advanced) {
        if (array !== null) pages[index] = `[${array[offset]}]`;
        else {
          const remaining = Buffer.from(values[index]).subarray(offset).toString("utf8");
          const end = kinds[index] === "rows" ? remaining.indexOf("\n") + 1 : (remaining.codePointAt(0) ?? 0) > 0xffff ? 2 : 1;
          pages[index] = remaining.slice(0, end || remaining.length);
        }
        return {text: "", position: request.position, complete: false, neededTokens: countGPT5Tokens(frame())};
      }
      break;
    }
    advanced = true;
    if (position[index] < size(index)) break;
  }
  return {text: frame(), position, complete: position.every((offset, index) => !included(index) || offset === size(index))};
}
export function createMReadTool(): NativeTool {
  return {
    specification: {
      type: "custom",
      name: "mread",
      description: "Continue omitted retained output without rerunning its producer. Only emitted references recover retained output; ordinary exec_command truncation has no mread recovery. Usage: `mread REF [REF ...] [--stdout|--stderr] [--max-tokens N]`. REF is a returned handle, not a path or range. Multiple handles share one budget and return one combined next_call. --stdout or --stderr selects one stream; otherwise both are returned. Source-row pages state their row range.",
    },
    nativeExecutor: "mread",
  };
}
