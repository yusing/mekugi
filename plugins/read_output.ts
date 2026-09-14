import {countGPT5Tokens} from "./tokens.ts";
import {selectHRunText} from "./hrun.ts";

type ReadKind = "" | "rows" | "json";
export type ReadPageRequest = {
  stdout: string; stderr: string;
  stdoutKind: ReadKind; stderrKind: ReadKind;
  position: [number, number]; stream: "" | "stdout" | "stderr";
};
export type ReadPage = {text: string; position: [number, number]; complete: boolean};

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

// One pagination owner selects bytes, whole verified rows, or complete JSON
// entries. The token budget includes the stream frames, not the next-call notice.
export function selectReadOutput(request: ReadPageRequest, budget: number): ReadPage {
  const values = [request.stdout, request.stderr];
  const kinds = [request.stdoutKind, request.stderrKind];
  const position: [number, number] = [...request.position];
  const pages = ["", ""];
  const entries = values.map((value, index): string[] | null => kinds[index] === "json" ? jsonArrayEntries(value) : null);
  const included = (index: number): boolean => request.stream === "" || request.stream === ["stdout", "stderr"][index];
  const size = (index: number): number => entries[index]?.length ?? Buffer.byteLength(values[index]);
  const frame = (): string => pages.map((page, index) => included(index)
    ? `--- ${["stdout", "stderr"][index]} [${kinds[index] || "bytes"}] ---\n${page}\n` : "").join("");
  const fits = (): boolean => countGPT5Tokens(frame()) <= budget;
  for (let index = 0; index < 2; index++) {
    const offset = position[index];
    if (!Number.isSafeInteger(offset) || offset < 0 || offset > size(index)) throw new Error("invalid read position");
    if (!included(index)) continue;
    if (entries[index] !== null) pages[index] = "[]";
  }
  if (!fits()) throw new Error("token budget cannot admit the stream frames; increase --max-tokens");
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
      pages[index] = "[" + array.slice(offset, offset + low).join(",") + "]";
      position[index] += low;
    } else {
      const bytes = Buffer.from(values[index]);
      if (offset < bytes.length && (bytes[offset] & 0xc0) === 0x80) throw new Error("read position splits UTF-8");
      if (kinds[index] === "rows" && offset > 0 && bytes[offset - 1] !== 10) throw new Error("read position splits a verified row");
      let end = Math.min(bytes.length, offset + budget * 128 + 4);
      while (end < bytes.length && (bytes[end] & 0xc0) === 0x80) end--;
      const remaining = bytes.subarray(offset, end).toString("utf8");
      let allowance = budget;
      while (allowance > 0) {
        let selected = selectHRunText(remaining, allowance, false).text;
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
      if (!advanced) throw new Error("next complete read unit does not fit; increase --max-tokens or use source preview");
      break;
    }
    advanced = true;
    if (position[index] < size(index)) break;
  }
  return {text: frame(), position, complete: position.every((offset, index) => !included(index) || offset === size(index))};
}
