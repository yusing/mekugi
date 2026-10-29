import type { Entry, Value } from "./protocol";

// Tool verbs fall into three families that carry the transcript's colour:
// commands that run, reads that inspect, and writes that change the workspace.
export type Family = "run" | "read" | "write" | "fail";

const families: Record<string, Family> = {
  run: "run",
  ran: "run",
  running: "run",
  check: "run",
  "run javascript": "run",
  custom: "run",
  mcp: "run",
  read: "read",
  view: "read",
  search: "read",
  list: "read",
  status: "read",
  inspect: "read",
  diff: "read",
  skill: "read",
  attached: "read",
  "attached skill": "read",
  edit: "write",
  edited: "write",
  create: "write",
  created: "write",
  delete: "write",
  deleted: "write",
  move: "write",
  moved: "write",
  commit: "write",
  committed: "write",
  stage: "write",
  conflict: "write",
  error: "fail",
  "attach failed": "fail",
};

export function family(verb: string | undefined): Family | undefined {
  return verb ? families[verb.toLowerCase()] : undefined;
}

export function failed(value: Value): boolean {
  return (value.exitCode ?? 0) !== 0 || !!value.hook?.hasError || value.hook?.status === "failed";
}

export function isUser(entry: Entry): boolean {
  return !!entry.turn;
}

export function reasoningBody(body = "", title?: string): string {
  const lead = title && `**${title}**`;
  return (lead && body.startsWith(lead) ? body.slice(lead.length) : body).trim();
}

export function hasDetails(entry: Entry, owner = "/root", labelClipped = false): boolean {
  return (
    labelClipped ||
    !!entry.values?.some(
      (value) =>
        value.detailsAvailable &&
        (!value.detailsAvailable.owner || value.detailsAvailable.owner === owner),
    )
  );
}

// An image artifact has a bound source, never inferred from a path label.
export function isView(entry: Entry): boolean {
  return (
    entry.kind === "tool" &&
    !!entry.images?.length &&
    entry.values?.length === 1 &&
    ["View", "Generated"].includes(entry.values[0].verb ?? "")
  );
}

// Delivered replies retain their complete authored body alongside summary/details.
export function responseText(kind?: string, text = "", body = ""): string {
  const response = kind === "reply" ? body : kind === "text" || kind === "final" ? text : "";
  return response.trim() ? response : "";
}
