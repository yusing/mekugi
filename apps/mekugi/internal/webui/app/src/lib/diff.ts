// A unified diff as rows to paint. Hunk rows restart the line numbers and
// mark the gaps between hunks; file headers before the first hunk are left
// out, since the pane names the file.
export interface DiffRow {
  kind: "hunk" | "note" | "add" | "remove" | "context";
  text: string;
  old?: number;
  new?: number;
}

const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

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
      if (!line.startsWith("--- ") && !line.startsWith("+++ "))
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
