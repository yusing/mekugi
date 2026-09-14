const maxRetainedBytes = 16 * 1024 * 1024;

export class RetainedRows {
  #rows: string[] = [];
  #bytes = 0;

  append(row: string): boolean {
    const bytes = Buffer.byteLength(row);
    if (this.#bytes + bytes > maxRetainedBytes) {
      return false;
    }
    this.#rows.push(row);
    this.#bytes += bytes;
    return true;
  }

  remainder(shown: string): string {
    const complete = this.#rows.join("");
    if (!complete.startsWith(shown)) {
      throw new Error("displayed rows are not a prefix of retained output");
    }
    return complete.slice(shown.length);
  }
}
