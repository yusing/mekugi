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
      ...(item.reference === undefined ? {} : { path: item.reference }),
      data,
    });
    read += item.file.size;
    progress(read, total);
  }
  return result;
}
