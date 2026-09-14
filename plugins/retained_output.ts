import {mkdtempSync, openSync, writeFileSync, closeSync, unlinkSync, rmdirSync} from "node:fs";
import {tmpdir} from "node:os";
import path from "node:path";

const maxRetainedBytes = 16 * 1024 * 1024;

// These are ordinary executor-owned files, not executable @shell references.
// They remain readable after router shutdown until explicitly removed.
export function retainOutput(stdout: string, stderr: string, offsets = [0, 0], exitCode?: number): {directory: string; stdout: string; stderr: string} {
  if (offsets.length !== 2 || offsets.some((offset, index) => !Number.isSafeInteger(offset) || offset < 0
      || offset > Buffer.byteLength(index === 0 ? stdout : stderr))) {
    throw new Error("invalid retained output offset");
  }
  if (Buffer.byteLength(stdout) + Buffer.byteLength(stderr) > maxRetainedBytes) {
    throw new Error("retained output exceeds 16 MiB");
  }
  const directory = mkdtempSync(path.join(tmpdir(), "mko-"));
  const files = {stdout: path.join(directory, "stdout"), stderr: path.join(directory, "stderr")};
  const created: string[] = [];
  try {
    const metadata = JSON.stringify({stdout: {file: "stdout", unread_byte: offsets[0]},
      stderr: {file: "stderr", unread_byte: offsets[1]}, ...(exitCode === undefined ? {} : {exit_code: exitCode})});
    for (const [name, content] of [[files.stdout, stdout], [files.stderr, stderr],
      [path.join(directory, "metadata.json"), metadata]]) {
      const fd = openSync(name, "wx", 0o600);
      created.push(name);
      try {
        writeFileSync(fd, content, "utf8");
      } finally {
        closeSync(fd);
      }
    }
    return {directory, ...files};
  } catch (error) {
    for (const file of created) {
      try { unlinkSync(file); } catch {}
    }
    try { rmdirSync(directory); } catch {}
    throw error;
  }
}

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

  save(): string {
    return retainOutput(this.#rows.join(""), "").stdout;
  }
}
