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

export function hasDetails(entry: Entry, owner = "Main", labelClipped = false): boolean {
  // Authored content is complete inline, or has its own measured disclosure.
  if (
    ["user", "text", "final", "reply", "message", "start", "assignment", "reasoning"].includes(
      entry.kind ?? "",
    ) ||
    isUser(entry) ||
    (entry.kind === "error" && entry.agent !== owner && entry.agent !== "Main")
  )
    return false;
  return !!entry.values?.some(
    (value) =>
      // Progress renders its complete body (and owns its wait-target disclosure).
      // Its retained tool envelope is not shown by the browser details panel.
      (value.kind !== "progress" &&
        (value.detailsAvailable ||
          (value.detailCode?.trim() && value.detailCode.trim() !== value.code?.trim()))) ||
      value.tailClipped ||
      value.sourceOmitted ||
      (!value.questions?.length && value.kind !== "progress" && value.body?.trim()) ||
      (value.code?.trim() && value.label && !value.fenced && !value.code.includes("\n")) ||
      (value.tailOmitted ?? 0) > 0 ||
      value.changes?.length ||
      (value.hook &&
        (labelClipped ||
          (!failed(value) && (value.tailLines || value.tail?.some((line) => line.trim()))))) ||
      ["Create", "Edit", "Delete", "Move"].includes(value.verb ?? ""),
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

// A tool row that states only its summary: no block of code, output, changes,
// questions or waits shows beneath it until a disclosure opens. Its entry
// draws it as a bare trace line instead of a card.
export function summaryOnly(value: Value): boolean {
  const hasOutput = !!value.tailLines || !!value.tail?.some((line) => line.trim());
  const tailLines = value.tailLines ?? value.tail?.length ?? 0;
  if (hasOutput && tailLines <= 1 && !value.tailOmitted) return false;
  const code = value.code?.replace(/\n$/, "");
  const multiline = (value.codeLines ?? 0) > 1 || (code !== undefined && code.includes("\n"));
  const folded = multiline || (value.lang === "json" && (!!code || !!value.codeLines));
  const headline = !value.label && !value.reads?.length && !!code;
  if (!folded && (value.codeLines || (code && value.fenced && !headline))) return false;
  return (
    (value.reads?.length ?? 0) <= 1 &&
    !value.changes?.length &&
    !value.questions?.length &&
    !value.waits?.length
  );
}
