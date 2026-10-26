import { parseSelection, selectionHash, type Selection } from "./transport";
import { semantic } from "./semantic.svelte";
import { stream } from "./stream.svelte";

// Connection, logical thread and agent together identify this viewer's selection.
export const view = $state<Selection>(parseSelection(location.hash));

function restore(): void {
  Object.assign(view, parseSelection(location.hash));
}
addEventListener("popstate", restore);
addEventListener("hashchange", restore);

function go(selection: Selection, replace: boolean): void {
  const url = selectionHash(selection) || location.pathname + location.search;
  if (replace) history.replaceState(null, "", url);
  else history.pushState(null, "", url);
  Object.assign(view, { machine: undefined, session: undefined, agent: undefined }, selection);
}

// Without a thread, the main area offers a new thread on the machine.
export function select(machine: string | undefined): void {
  if (machine !== view.machine || view.session) go({ machine }, false);
}

export function show(agent: string | undefined, replace = false): void {
  if (agent === "Main") agent = "/root";
  const family = !stream.playback ? semantic.frame?.family : undefined;
  const thread = !agent || agent === "/root" ? family?.root : stream.agent(agent)?.thread;
  if (thread && (!agent || agent === "/root" || family?.threads.includes(thread))) {
    open(thread, replace);
    return;
  }
  if (agent === "/root") agent = undefined;
  if (agent !== view.agent) go({ ...view, agent }, replace);
}

export function open(session: string | undefined, replace = false): void {
  if (session !== view.session || view.agent) go({ machine: view.machine, session }, replace);
}
