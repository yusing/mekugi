import {getSessionMessages, getSubagentMessages, type SessionMessage} from '@anthropic-ai/claude-agent-sdk';
import type {ContextSelection, HistorySelection} from './session_history.js';

type ContextMessage = SessionMessage & {isCompactSummary?: boolean; timestamp?: string};
type ReadSnapshot = () => Promise<ContextMessage[]>;
const messageLimit = 32768;
const byteLimit = 64 << 20;

// The SDK materializes the selected native chain before limit/offset slicing.
// One sentinel-bounded snapshot avoids repeated reads and cross-page drift.
export async function scanSkillHistory(read: ReadSnapshot, selection?: string[]): Promise<ContextMessage[]> {
  const allowed = selection ? new Set(selection) : undefined;
  const seen = new Set<string>();
  const selected: ContextMessage[] = [];
  let bytes = 0;
  if (allowed?.size === 0) return selected;
  const snapshot = await read();
  for (const message of snapshot.slice(0, messageLimit)) {
    bytes += Buffer.byteLength(JSON.stringify(message));
    if (bytes > byteLimit) throw new Error('Current-context history exceeds the observation byte limit');
    if (!message.uuid || seen.has(message.uuid)) throw new Error('Current-context history returned missing or repeated message identity');
    seen.add(message.uuid);
    if (!allowed || allowed.has(message.uuid)) selected.push(message);
    if (allowed && selected.length === allowed.size) return selected;
  }
  if (snapshot.length > messageLimit) throw new Error('Current-context history exceeds the observation message limit');
  if (allowed && selected.length !== allowed.size) throw new Error('Saved child current-context selection is incomplete');
  return selected;
}

// The SDK can relink pre-compaction tool records after the summary to retain
// native conversation history. They are not new loads in the cleared context.
function currentSkillContext(messages: ContextMessage[]): ContextMessage[] {
  const summaries = messages.filter(message => message.isCompactSummary);
  if (!summaries.length) return messages;
  const times = summaries.map(message => Date.parse(message.timestamp ?? ''));
  if (times.some(time => !Number.isFinite(time))) throw new Error('Native compact-summary timestamp is unavailable');
  const boundary = Math.max(...times);
  return messages.filter(message => {
    const time = Date.parse(message.timestamp ?? '');
    if (!Number.isFinite(time)) throw new Error('Native current-context message timestamp is unavailable');
    return time >= boundary;
  });
}

export async function restoreSkillHistory(sessionID: string, cwd: string, children: ContextSelection[], emit: (frame: unknown) => Promise<void>, displayed: HistorySelection[] = []): Promise<HistorySelection[]> {
  const retained: HistorySelection[] = [];
  let remaining = messageLimit;
  const targets: ContextSelection[] = [{sessionID, agentID: ''}, ...children];
  for (const target of targets) {
    const agentID = target.agentID;
    // Auxiliary count failures must not discard independently selected history.
    const fallback = displayed.find(child => child.sessionID === target.sessionID && child.agentID === agentID);
    let selection: HistorySelection = target.messageIDs
      ? {...target, messageIDs: target.messageIDs}
      : {...target, messageIDs: fallback?.messageIDs ?? [], complete: false};
    await emit({kind: 'skill_history', agentID, phase: 'start'});
    try {
      if (target.messageIDs && target.complete !== true) throw new Error('Saved child context completeness is unavailable');
      const messages = await scanSkillHistory(() => agentID
        ? getSubagentMessages(target.sessionID, agentID, {dir: cwd, limit: messageLimit + 1})
        : getSessionMessages(sessionID, {dir: cwd, limit: messageLimit + 1}), target.messageIDs);
      if (!messages.length && (!target.messageIDs || target.messageIDs.length)) throw new Error('Native current-context history is unavailable');
      if (agentID) {
        if (messages.length > remaining) throw new Error('Retained child context exceeds the observation message limit');
        selection = {...target, messageIDs: messages.map(message => message.uuid), complete: true};
        await emit({kind: 'saved_agent', id: agentID, callers: [...new Set(messages.map(message => message.parent_tool_use_id).filter(Boolean))]});
      }
      for (const message of currentSkillContext(messages)) {
        // Prose is not load evidence. Keep only native tool blocks and explicit
        // compact-summary metadata; never infer a boundary from its text.
        const content = (message.message as {content?: unknown} | undefined)?.content;
        const blocks = Array.isArray(content) ? content.filter(block => block?.type === 'tool_use' || block?.type === 'tool_result') : [];
        if (!message.isCompactSummary && !blocks.length) continue;
        const event = {...message, message: {content: blocks}};
        const frame = {kind: 'skill_history', agentID, phase: 'message', event};
        if (Buffer.byteLength(JSON.stringify(frame)) >= 8 << 20) throw new Error('Current-context message exceeds the transport limit');
        await emit(frame);
      }
      await emit({kind: 'skill_history', agentID, phase: 'done'});
    } catch (error) {
      await emit({kind: 'skill_history', agentID, phase: 'done', failed: true});
      await emit({kind: 'notice', text: `Could not restore active skills for ${agentID || 'Main'}: ${String(error)}; the count is unavailable`});
    }
    if (agentID) {
      retained.push(selection);
      remaining -= selection.messageIDs.length;
    }
  }
  return retained;
}
