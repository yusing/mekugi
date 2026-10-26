import { duration, progressText } from "./format";
import { inline } from "./markdown";
import type { Agent, Entry } from "./protocol";

// What an entry shows its agent doing, as a verb and inline HTML without
// links, since it is shown inside a control. A final answer is finished work.
export function doing(entry: Entry): { verb: string; html: string } | undefined {
  if (entry.kind === "final") return undefined;
  const { verb, html } = describe(entry);
  return { verb, html: html.replace(/<\/?a\b[^>]*>/g, "") };
}

function describe(entry: Entry): { verb: string; html: string } {
  const value = entry.values?.at(-1);
  if (value?.kind === "progress")
    return {
      verb: "",
      html: inline(
        progressText(value, (name) => (name === "/root" ? "main" : name.replace(/^\/root\//, ""))),
      ),
    };
  if (entry.kind === "reasoning") {
    const labels = entry.values?.filter((v) => v.kind === "summary" && v.label).map((v) => v.label);
    if (labels?.length) return { verb: "", html: inline(labels.join(", ")) };
    const body = value?.body?.trim();
    return {
      verb: value?.live ? "Thinking" : "Thought",
      html: body && !body.includes("\n") && body.length <= 80 ? inline(body) : "",
    };
  }
  if (entry.kind === "text" || entry.kind === "message") return { verb: "Writing", html: "" };
  if (!value) return { verb: entry.kind ?? "", html: "" };
  const subject =
    value.label ??
    value.reads?.map((r) => `\`${r.path}\``).join(", ") ??
    (value.code ? `\`${value.code.split("\n")[0]}\`` : "");
  return { verb: value.verb ?? value.kind ?? entry.kind ?? "", html: inline(subject) };
}

// Work time on the recorded clock, as the terminal's roster timer counts it.
export function worked(agent: Agent, now: number): number | undefined {
  if (agent.work === undefined) return undefined;
  return agent.work + (agent.workSince ? Math.max(0, now - agent.workSince) : 0);
}

export function since(at: number | undefined, now: number): string | undefined {
  if (!at) return undefined;
  const age = Math.max(0, now - at);
  return age < 2000 ? "just now" : `${duration(age)} ago`;
}

export const stateLabel: Record<Agent["state"], string> = {
  working: "Working",
  waiting: "Waiting",
  error: "Error",
  done: "Done",
  idle: "Idle",
};
