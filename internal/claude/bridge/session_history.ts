import {getSessionMessages, getSubagentMessages, listSubagents, type SessionMessage} from '@anthropic-ai/claude-agent-sdk';
import {companionRequest, type CompanionConfig} from './companion_transport.js';

export interface HistorySelection extends ContextSelection {messageIDs: string[]}
export interface SavedAgent {id: string; callers: string[]}
export interface ContextSelection {sessionID: string; agentID: string; messageIDs?: string[]; complete?: boolean}

// Native transcripts select completed display rows. Child reads do not create
// tasks, bind agents, or supply execution authority to historical callers.
export async function sessionHistory(sessionID: string, cwd: string, endpoint?: CompanionConfig): Promise<{messages: SessionMessage[]; notices: string[]; children: HistorySelection[]; agents: SavedAgent[]; contexts: ContextSelection[]}> {
  const root = await getSessionMessages(sessionID, {dir: cwd, limit: 2001});
  if (!root.length) throw new Error('Native resume history is unavailable');
  const messages = root.slice(0, 2000);
  const notices: string[] = [];
  let limited = root.length > 2000;
  const selected: HistorySelection[] = [];
  const agents: SavedAgent[] = [];
  let inherited: HistorySelection[] = [];
  if (endpoint) {
    try {
      const result = await companionRequest(endpoint, {operation: 'history_read', binding: {runtime: 'claude', workspace: cwd, session: sessionID}}, undefined, 8 << 20) as {children: HistorySelection[] | null};
      inherited = result.children ?? [];
    } catch (error) { notices.push(`Saved native child history selection unavailable: ${String(error)}`); }
  }
  let children: string[];
  try { children = await listSubagents(sessionID, {dir: cwd}); }
  catch (error) {
    notices.push(`Native child history unavailable: ${String(error)}`);
    children = [];
  }
  const targets = new Map<string, ContextSelection>();
  for (const selection of inherited) targets.set(JSON.stringify([selection.sessionID, selection.agentID]), selection);
  for (const agentID of children) targets.set(JSON.stringify([sessionID, agentID]), {sessionID, agentID});
  limited ||= targets.size > 128;
  for (const child of [...targets.values()].slice(0, 128)) {
    try {
      const remaining = 2000 - messages.length;
      if (!remaining) { limited = true; break; }
      const history = await getSubagentMessages(child.sessionID, child.agentID, {dir: cwd, limit: child.messageIDs ? 2001 : remaining + 1});
      const allowed = child.messageIDs ? new Set(child.messageIDs) : undefined;
      const eligible = allowed ? history.filter(message => allowed.has(message.uuid)) : history;
      const shown = eligible.slice(0, remaining);
      messages.push(...shown);
      if (shown.length) agents.push({id: child.agentID, callers: [...new Set(shown.map(message => message.parent_tool_use_id).filter((caller): caller is string => Boolean(caller)))]});
      if (shown.length) selected.push({sessionID: child.sessionID, agentID: child.agentID, messageIDs: shown.map(message => message.uuid)});
      if (allowed && eligible.length !== allowed.size) {
        if (history.length > 2000) limited = true;
        else notices.push(`Saved native child history ${child.agentID} is incomplete`);
      }
      limited ||= eligible.length > remaining;
    } catch (error) {
      notices.push(`Native child history ${child.agentID} unavailable: ${String(error)}`);
    }
  }
  if (limited) notices.push('Transcript display limited to 2000 messages and 128 children; native resume retains its own context');
  return {messages, notices, children: selected, agents, contexts: [...targets.values()].slice(0, 128)};
}
