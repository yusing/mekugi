import { duration, progressText } from "./format";
import { inline } from "./markdown";
import type { Agent, Entry } from "./protocol";

export function agentDisplayName(name: string): string {
  if (name === "/root" || name === "agent:main" || name === "Main") return "main";
  return name.replace(/^(?:\/root\/|agent:main\/)/, "");
}

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
      html: inline(progressText(value, agentDisplayName)),
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

// The terminal's role colours: twelve chosen xterm colours, then the rest of
// its colour cube. The nth role observed takes the nth colour.
// Source: modules/activity/terminalview/roles.go:12:30@[23a0c8ad] (*View).RoleColor
const rolePalette = [75, 214, 141, 114, 203, 80, 180, 217, 111, 149, 220, 159];
for (let color = 16; color < 232; color++)
  if (!rolePalette.includes(color)) rolePalette.push(color);
const cube = [0, 95, 135, 175, 215, 255];

const roleColors = rolePalette.map((entry) => {
  const color = entry - 16;
  const rgb = [Math.floor(color / 36), Math.floor(color / 6) % 6, color % 6].map(
    (level) => cube[level],
  );
  const hex = `#${rgb.map((channel) => channel.toString(16).padStart(2, "0")).join("")}`;
  const luminance = rgb
    .map((channel) => channel / 255)
    .map((channel) => (channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4))
    .reduce((sum, channel, i) => sum + channel * [0.2126, 0.7152, 0.0722][i], 0);
  // The cube includes black and deep blues. Below this luminance they cannot
  // reach 4.5:1 on dark role tags (#29263c). Keep their light appearance and
  // stable assignment, using ordinary text ink only where they cannot be read.
  return luminance < 0.28 ? `light-dark(${hex}, var(--text))` : hex;
});

export function roleColor(index: number): string {
  return roleColors[index % roleColors.length];
}

export const stateLabel: Record<Agent["state"], string> = {
  working: "Working",
  waiting: "Waiting",
  error: "Error",
  done: "Done",
  idle: "Idle",
};
