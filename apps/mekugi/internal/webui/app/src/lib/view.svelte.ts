import { parseSelection, selectionHash, type Selection } from "./transport";

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
  if (agent === "/root") agent = undefined;
  if (agent !== view.agent) go({ ...view, agent }, replace);
}

export function open(session: string | undefined, replace = false): void {
  if (session !== view.session || view.agent) go({ machine: view.machine, session }, replace);
}
