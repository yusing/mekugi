import type { SyntaxToken } from "./highlight";

// A unified diff as rows to paint. Hunk rows restart the line numbers and
// mark the gaps between hunks; file headers before the first hunk are left
// out, since the pane names the file.
export interface DiffRow {
  kind: "hunk" | "note" | "add" | "remove" | "context";
  text: string;
  old?: number;
  new?: number;
}

export interface WordSpan {
  start: number;
  end: number;
}
export interface DiffToken extends SyntaxToken {
  changed?: boolean;
}

const maxWordTokens = 2048;
// Repeated tokens can require many recursive searches even in a small block.
const maxWordWork = 50_000;
const maxSource = 64 * 1024;
const graphemes = new Intl.Segmenter(undefined, { granularity: "grapheme" });

// Match the terminal's identifier/whitespace/grapheme tokens. UTF-16 offsets
// are used only at grapheme boundaries, so emoji and combining marks stay whole.
function wordTokens(source: string): string[] | undefined {
  const tokens: string[] = [];
  let start = 0,
    previous = -1;
  for (const { segment, index } of graphemes.segment(source)) {
    const kind = /^[\p{L}\p{N}\p{M}_]/u.test(segment) ? 1 : /^\s/u.test(segment) ? 2 : 0;
    if (index > start && (kind !== previous || kind === 0)) {
      tokens.push(source.slice(start, index));
      if (tokens.length > maxWordTokens) return undefined;
      start = index;
    }
    previous = kind;
  }
  tokens.push(source.slice(start));
  return tokens;
}

// SequenceMatcher's longest contiguous matches, with whitespace excluded as
// an anchor but included beside matching words. This aligns replacement blocks
// across rows rather than comparing arbitrary pairs of lines.
function changedWords(before: string[], after: string[]): [WordSpan[], WordSpan[]] | undefined {
  const positions = new Map<string, number[]>();
  after.forEach((token, i) => {
    if (!token.trim()) return;
    const indexes = positions.get(token) ?? [];
    indexes.push(i);
    positions.set(token, indexes);
  });
  const matches: [number, number, number][] = [];
  const pending = [[0, before.length, 0, after.length]];
  let work = 0;
  while (pending.length) {
    const [a0, a1, b0, b1] = pending.pop()!;
    let ai = a0,
      bi = b0,
      size = 0;
    let lengths = new Map<number, number>();
    for (let i = a0; i < a1; i++) {
      if (++work > maxWordWork) return undefined;
      const next = new Map<number, number>();
      for (const j of positions.get(before[i]) ?? []) {
        if (++work > maxWordWork) return undefined;
        if (j < b0) continue;
        if (j >= b1) break;
        const n = (lengths.get(j - 1) ?? 0) + 1;
        next.set(j, n);
        if (n > size) {
          ai = i - n + 1;
          bi = j - n + 1;
          size = n;
        }
      }
      lengths = next;
    }
    while (ai > a0 && bi > b0 && !after[bi - 1].trim() && before[ai - 1] === after[bi - 1]) {
      ai--;
      bi--;
      size++;
    }
    while (
      ai + size < a1 &&
      bi + size < b1 &&
      !after[bi + size].trim() &&
      before[ai + size] === after[bi + size]
    )
      size++;
    if (!size) continue;
    matches.push([ai, bi, size]);
    if (a0 < ai && b0 < bi) pending.push([a0, ai, b0, bi]);
    if (ai + size < a1 && bi + size < b1) pending.push([ai + size, a1, bi + size, b1]);
  }
  matches.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  matches.push([before.length, after.length, 0]);
  const result: [WordSpan[], WordSpan[]] = [[], []];
  for (const [side, tokens] of [before, after].entries()) {
    const offsets = [0];
    for (const token of tokens) offsets.push(offsets.at(-1)! + token.length);
    let end = 0;
    for (const match of matches) {
      const at = match[side];
      if (at > end) result[side].push({ start: offsets[end], end: offsets[at] });
      end = at + match[2];
    }
  }
  return result;
}

export function diffWords(rows: DiffRow[]): Map<number, WordSpan[]> {
  const words = new Map<number, WordSpan[]>();
  for (let i = 0; i < rows.length;) {
    if (rows[i].kind !== "add" && rows[i].kind !== "remove") {
      i++;
      continue;
    }
    const sides: number[][] = [[], []];
    while (
      i < rows.length &&
      (rows[i].kind === "add" || rows[i].kind === "remove" || rows[i].kind === "note")
    ) {
      if (rows[i].kind !== "note") sides[rows[i].kind === "remove" ? 0 : 1].push(i);
      i++;
    }
    if (sides.some((side) => !side.length)) continue;
    if (
      sides.reduce(
        (size, side) => size + side.reduce((n, index) => n + rows[index].text.length + 1, 0),
        0,
      ) > maxSource
    )
      continue;
    const sources = sides.map((side) => side.map((index) => rows[index].text + "\n").join(""));
    const before = wordTokens(sources[0]),
      after = wordTokens(sources[1]);
    if (!before || !after || before.length + after.length > maxWordTokens) continue;
    const changes = changedWords(before, after);
    if (!changes) continue;
    sides.forEach((side, n) => {
      let offset = 0,
        spanIndex = 0;
      for (const index of side) {
        const end = offset + rows[index].text.length;
        const spans: WordSpan[] = [];
        while (spanIndex < changes[n].length && changes[n][spanIndex].end <= offset) spanIndex++;
        for (let j = spanIndex; j < changes[n].length && changes[n][j].start < end; j++) {
          const span = changes[n][j];
          spans.push({
            start: Math.max(offset, span.start) - offset,
            end: Math.min(end, span.end) - offset,
          });
        }
        if (spans.length) words.set(index, spans);
        offset = end + 1;
      }
    });
  }
  return words;
}

export function diffTokens(
  text: string,
  syntax: SyntaxToken[] | undefined,
  words: WordSpan[] = [],
): DiffToken[] {
  const out: DiffToken[] = [];
  let offset = 0,
    spanIndex = 0;
  for (const token of syntax ?? [{ text }]) {
    const start = offset;
    const end = offset + token.text.length;
    while (offset < end) {
      while (spanIndex < words.length && words[spanIndex].end <= offset) spanIndex++;
      const span = words[spanIndex];
      const changed = !!span && span.start <= offset;
      const next = Math.min(end, span ? (changed ? span.end : span.start) : end);
      out.push({
        text: token.text.slice(offset - start, next - start),
        style: token.style,
        changed,
      });
      offset = next;
    }
  }
  return out;
}

const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

const operation = /^(add|update|delete|move) ".*" -> ".*"$/;

export function diffRows(diff: string): DiffRow[] {
  const rows: DiffRow[] = [];
  const lines = diff.split("\n");
  if (lines.at(-1) === "") lines.pop();
  let old = 0,
    now = 0,
    started = false;
  for (const line of lines) {
    const header = hunk.exec(line);
    if (header) {
      started = true;
      old = Number(header[1]);
      now = Number(header[2]);
      rows.push({ kind: "hunk", text: line });
    } else if (!started) {
      // File headers and the review's operation line repeat what the view
      // already says about the file.
      if (!line.startsWith("--- ") && !line.startsWith("+++ ") && !operation.test(line))
        rows.push({ kind: "note", text: line });
    } else if (line.startsWith("+")) rows.push({ kind: "add", text: line.slice(1), new: now++ });
    else if (line.startsWith("-")) rows.push({ kind: "remove", text: line.slice(1), old: old++ });
    else if (line.startsWith("\\")) rows.push({ kind: "note", text: line.slice(1).trim() });
    else rows.push({ kind: "context", text: line.slice(1), old: old++, new: now++ });
  }
  return rows;
}

// A path inside the workspace relative to it; any other path stays whole.
export function workspacePath(path: string, workspace: string): string {
  const root = workspace.replace(/\/+$/, "");
  return root && path.startsWith(`${root}/`) ? path.slice(root.length + 1) : path;
}
