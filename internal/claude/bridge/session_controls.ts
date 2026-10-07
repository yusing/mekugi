import {listSessions} from '@anthropic-ai/claude-agent-sdk';

export interface SessionListCommand {id: string; cwd?: string; cursor?: string; limit: number}

// SDK metadata owns enumeration and order. The shared UI's cursor is an offset,
// not a router-created session catalog or a search over rendered transcripts.
export async function sessionPage(command: SessionListCommand): Promise<{
  kind: 'sessions'; id: string; cursor: string;
  sessions: {ID: string; Title: string; Cwd: string; Branch: string; Updated: number}[];
}> {
  const offset = command.cursor ? Number(command.cursor) : 0;
  if (!Number.isSafeInteger(offset) || offset < 0 || !Number.isSafeInteger(command.limit) || command.limit < 1 || command.limit > 100) {
    throw new Error('Invalid native session page');
  }
  const rows = await listSessions({...(command.cwd ? {dir: command.cwd, includeWorktrees: false} : {}),
    includeProgrammatic: true, limit: command.limit + 1, offset});
  return {kind: 'sessions', id: command.id, cursor: rows.length > command.limit ? String(offset + command.limit) : '',
    sessions: rows.slice(0, command.limit).map(row => ({ID: row.sessionId, Title: row.customTitle ?? row.summary,
      Cwd: row.cwd ?? '', Branch: row.gitBranch ?? '', Updated: Math.floor(row.lastModified / 1000)}))};
}
