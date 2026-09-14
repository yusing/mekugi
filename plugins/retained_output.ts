export const MAX_RETAINED_BYTES = 16 * 1024 * 1024;

export class RetainedRows {
  #rows: string[] = [];
  #bytes = 0;

  append(row: string): boolean {
    const bytes = Buffer.byteLength(row);
    if (this.#bytes + bytes > MAX_RETAINED_BYTES) {
      return false;
    }
    this.#rows.push(row);
    this.#bytes += bytes;
    return true;
  }

  prefixBefore(shown: string): string {
    const complete = this.#rows.join("");
    if (!complete.endsWith(shown)) throw new Error("displayed rows are not a retained suffix");
    return complete.slice(0, complete.length - shown.length);
  }

  remainder(shown: string): string {
    const complete = this.#rows.join("");
    if (!complete.startsWith(shown)) {
      throw new Error("displayed rows are not a prefix of retained output");
    }
    return complete.slice(shown.length);
  }
}
