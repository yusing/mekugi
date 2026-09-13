// Bun bundles these synchronous imports lazily. Registry validation, translation,
// argument failures, and line-only readers do not need the tokenizer tables.
let model: typeof import("gpt-tokenizer/model/gpt-5") | undefined;
let vocabulary: typeof import("gpt-tokenizer/bpeRanks/o200k_base").default | undefined;
const tokenizer = () => model ??= require("gpt-tokenizer/model/gpt-5") as NonNullable<typeof model>;
const tokenRanks = () => vocabulary ??= require("gpt-tokenizer/bpeRanks/o200k_base").default as NonNullable<typeof vocabulary>;
import {O200K_TOKEN_SPLIT_REGEX} from "gpt-tokenizer/encodingParams/constants";

// Source and command output treat tokenizer control spellings as ordinary text.
const sourceTokenOptions = {disallowedSpecial: new Set<string>()};
export const MAX_POSSIBLE_GPT5_TOKEN_BYTES = 128;
const LONG_PIECE_BYTES = 4096;
const longPieceCache = new Map<string, number[]>();
const LONG_PIECE_CACHE_BYTES = 4 * 1024 * 1024;
let cachedPieceBytes = 0;
let byteRanks: Map<string, number> | undefined;

export function tokenBytes(token: number): Buffer {
  const value = tokenRanks()[token];
  if (value === undefined) {
    throw new Error(`unknown GPT-5 token ${token}`);
  }
  return typeof value === "string" ? Buffer.from(value, "utf8") : Buffer.from(value);
}

function ranksByBytes(): Map<string, number> {
  if (byteRanks === undefined) {
    byteRanks = new Map();
    tokenRanks().forEach((_value, rank) => byteRanks!.set(tokenBytes(rank).toString("latin1"), rank));
  }
  return byteRanks;
}

// The pinned tokenizer scans every remaining pair for each merge, which is
// quadratic for long words or whitespace. A heap selects the same lowest rank,
// breaking ties at the leftmost byte, while linked offsets avoid array shifts.
// This is the large-piece strategy used by OpenAI tiktoken's _byte_pair_merge_large.
function encodeLongPiece(piece: string): number[] {
  const cached = longPieceCache.get(piece);
  if (cached !== undefined) {
    return cached;
  }
  const source = Buffer.from(piece, "utf8");
  const bytes = source.toString("latin1");
  const length = bytes.length;
  const ranks = ranksByBytes();
  const next = new Uint32Array(length);
  const previous = new Int32Array(length);
  const pending = new Uint32Array(length);
  const absent = 0xffffffff;
  pending.fill(absent);
  const heap: number[] = [];
  const stride = length + 1;

  // rank * stride + offset is exact within the bounded reader/command inputs.
  const push = (key: number): void => {
    let index = heap.length;
    heap.push(key);
    while (index > 0) {
      const parent = (index - 1) >> 1;
      if (heap[parent] <= key) {
        break;
      }
      heap[index] = heap[parent];
      index = parent;
    }
    heap[index] = key;
  };
  const pop = (): number => {
    const result = heap[0];
    const last = heap.pop()!;
    if (heap.length === 0) {
      return result;
    }
    let index = 0;
    while (index * 2 + 1 < heap.length) {
      let child = index * 2 + 1;
      if (child + 1 < heap.length && heap[child + 1] < heap[child]) {
        child += 1;
      }
      if (heap[child] >= last) {
        break;
      }
      heap[index] = heap[child];
      index = child;
    }
    heap[index] = last;
    return result;
  };
  const update = (index: number): void => {
    const right = next[index];
    const end = right < length ? next[right] : length;
    const rank = right < length && end - index <= MAX_POSSIBLE_GPT5_TOKEN_BYTES
      ? ranks.get(bytes.slice(index, end))
      : undefined;
    pending[index] = rank ?? absent;
    if (rank !== undefined) {
      push(rank * stride + index);
    }
  };
  for (let index = 0; index < length; index += 1) {
    next[index] = index + 1;
    previous[index] = index - 1;
  }
  for (let index = 0; index < length - 1; index += 1) {
    update(index);
  }
  while (heap.length > 0) {
    const key = pop();
    const left = key % stride;
    const rank = Math.floor(key / stride);
    if (pending[left] !== rank) {
      continue;
    }
    const right = next[left];
    next[left] = next[right];
    pending[right] = absent;
    if (next[left] < length) {
      previous[next[left]] = left;
    }
    update(left);
    if (previous[left] >= 0) {
      update(previous[left]);
    }
  }
  const result: number[] = [];
  for (let index = 0; index < length; index = next[index]) {
    const rank = ranks.get(bytes.slice(index, next[index]));
    if (rank === undefined) {
      throw new Error("unrecognized GPT-5 byte pair");
    }
    result.push(rank);
  }
  // Reader admission repeatedly counts growing windows with unchanged pieces.
  // Bound this cache by bytes, and own its keys rather than retaining slices of
  // much larger source strings.
  if (source.length <= LONG_PIECE_CACHE_BYTES) {
    while (cachedPieceBytes + source.length > LONG_PIECE_CACHE_BYTES) {
      const oldest = longPieceCache.keys().next().value!;
      cachedPieceBytes -= Buffer.byteLength(oldest, "utf8");
      longPieceCache.delete(oldest);
    }
    longPieceCache.set(source.toString("utf8"), result);
    cachedPieceBytes += source.length;
  }
  return result;
}

export function encodeGPT5(value: string): number[] {
  if (Buffer.byteLength(value, "utf8") <= LONG_PIECE_BYTES) {
    return tokenizer().encode(value, sourceTokenOptions);
  }
  const result: number[] = [];
  // Reuse the pinned model's exact pre-tokenization regex and vocabulary.
  for (const [piece] of value.matchAll(O200K_TOKEN_SPLIT_REGEX)) {
    const tokens = Buffer.byteLength(piece, "utf8") > LONG_PIECE_BYTES
      ? encodeLongPiece(piece)
      : tokenizer().encode(piece, sourceTokenOptions);
    for (const token of tokens) {
      result.push(token);
    }
  }
  return result;
}

export function countGPT5Tokens(value: string): number {
  return Buffer.byteLength(value, "utf8") <= LONG_PIECE_BYTES
    ? tokenizer().countTokens(value, sourceTokenOptions)
    : encodeGPT5(value).length;
}
