import type { Attachment } from "./protocol";

export type PendingAttachment =
  | { id: string; file: File; reference?: string }
  | { id: string; path: string };
export type InitialInput = {
  text: string;
  items: PendingAttachment[];
  attachments: Attachment[];
};
export const uploadLimit = 32 * 1024 * 1024;

export function referenceLabel(item: PendingAttachment): string | undefined {
  return "file" in item && item.reference !== undefined
    ? `[Selected ${item.file.name}]`
    : undefined;
}

export function activeAttachments(text: string, items: PendingAttachment[]): PendingAttachment[] {
  return items.filter((item) => {
    const label = referenceLabel(item);
    return !label || text.includes(label);
  });
}

// Retain reference bytes while editing, including after deletion for browser
// undo. Only explicitly captured, uniquely present markers bind on submission.
export function inlineReferences(text: string, items: PendingAttachment[]) {
  const references = items.flatMap((item) => {
    const label = referenceLabel(item);
    const start = label ? text.indexOf(label) : -1;
    return label && start >= 0 && text.indexOf(label, start + 1) < 0
      ? [{ start, end: start + label.length, item }]
      : [];
  });
  // ES2022 has no toSorted; this array belongs to this projection.
  // oxlint-disable-next-line unicorn/no-array-sort
  return references.sort((a, b) => a.start - b.start);
}

export function submissionAttachments(
  text: string,
  items: PendingAttachment[],
): PendingAttachment[] {
  return activeAttachments(text, items).filter((item) => {
    const label = referenceLabel(item);
    if (!label) return true;
    const start = text.indexOf(label);
    if (text.indexOf(label, start + 1) >= 0)
      throw new Error(
        `The reference ${label} appears more than once. Remove the duplicate before sending.`,
      );
    return true;
  });
}

export async function prepareAttachments(
  items: PendingAttachment[],
  signal: AbortSignal,
  progress: (read: number, total: number) => void,
): Promise<Attachment[]> {
  const total = items.reduce((size, item) => size + ("file" in item ? item.file.size : 0), 0);
  if (total > uploadLimit)
    throw new Error("Uploads exceed the 32 MiB limit. Remove files and try again.");
  let read = 0;
  progress(read, total);
  const result: Attachment[] = [];
  for (const item of items) {
    signal.throwIfAborted();
    if ("path" in item) {
      result.push({ source: "path", path: item.path });
      continue;
    }
    const data = await new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      const abort = () => reader.abort();
      signal.addEventListener("abort", abort, { once: true });
      reader.addEventListener("loadend", () => signal.removeEventListener("abort", abort), {
        once: true,
      });
      reader.addEventListener(
        "abort",
        () => reject(new DOMException("Attachment preparation cancelled.", "AbortError")),
        { once: true },
      );
      reader.addEventListener(
        "error",
        () => reject(new Error(`Could not read ${item.file.name}.`)),
        { once: true },
      );
      reader.addEventListener("progress", (event) => progress(read + event.loaded, total));
      reader.addEventListener("load", () => resolve(String(reader.result).split(",", 2)[1]), {
        once: true,
      });
      reader.readAsDataURL(item.file);
    });
    result.push({
      source: item.reference === undefined ? "upload" : "selection",
      name: item.file.name,
      ...(item.reference === undefined ? {} : { path: item.reference, inline: true }),
      data,
    });
    read += item.file.size;
    progress(read, total);
  }
  return result;
}
