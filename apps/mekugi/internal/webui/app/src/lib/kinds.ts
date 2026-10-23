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
  return entry.agent === "You" || !!entry.agent?.endsWith(" · user");
}

export function reasoningBody(body = "", title?: string): string {
  const lead = title && `**${title}**`;
  return (lead && body.startsWith(lead) ? body.slice(lead.length) : body).trim();
}

export function hasDetails(entry: Entry): boolean {
  if (entry.kind === "reasoning")
    return !!entry.values?.some((value) => reasoningBody(value.body, value.label));
  return (
    !!entry.text?.trim() ||
    !!entry.assignmentText?.trim() ||
    !!entry.values?.some(
      (value) =>
        value.body?.trim() ||
        value.code?.trim() ||
        value.tail?.some((line) => line.trim()) ||
        value.changes?.length ||
        value.questions?.length,
    )
  );
}
