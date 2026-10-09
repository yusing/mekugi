import {listSubagents, type SDKMessage} from '@anthropic-ai/claude-agent-sdk';
import {createServer, type Server} from 'node:http';
import {mkdtemp, mkdir, writeFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {randomUUID} from 'node:crypto';

export interface AgentMessage {id: string; sessionID: string; agentID: string; text: string}
export interface AgentMessageReceipt extends AgentMessage {kind: 'agent_message'; failed?: boolean}
interface Pending {command: AgentMessage; savedOwned: boolean; finish: (receipt: AgentMessageReceipt) => void; timer: ReturnType<typeof setTimeout>}
export interface NativeContextBoundary {id: string; sessionID: string; agentID: string; afterUUID?: string}

// SDK 0.3.288 has no supported direct-message Query control. Claude Code
// 2.1.288's official invocation-local mod API calls native session.send instead.
// The engine stamps plugin provenance and owns continuation and permissions.
export class AgentMessages {
  private readonly pending = new Map<string, Pending>();
  private readonly queued: string[] = [];
  private readonly boundaries = new Map<string, NativeContextBoundary>();
  private readonly drained = new Map<string, string>();
  private closed = false;
  private constructor(readonly plugin: string, private readonly server: Server,
    private readonly contextBoundary: (receipt: NativeContextBoundary) => Promise<void>) {}

  static async create(contextBoundary: (receipt: NativeContextBoundary) => Promise<void>): Promise<AgentMessages> {
    const plugin = await mkdtemp(join(tmpdir(), 'mekugi-agent-'));
    const socket = join(plugin, 'control.sock');
    const token = randomUUID();
    const server = createServer();
    const owner = new AgentMessages(plugin, server, contextBoundary);
    server.on('request', (request, response) => {
      response.setHeader('Content-Type', 'application/json');
      if (request.headers.authorization !== `Bearer ${token}`) {response.writeHead(403).end('{}'); return;}
      if (request.method === 'GET' && request.url === '/intent') {
        let pending: Pending | undefined;
        while (owner.queued.length && !pending) pending = owner.pending.get(owner.queued.shift()!);
        response.end(JSON.stringify(pending ? {...pending.command, savedOwned: pending.savedOwned} : {}));
        return;
      }
      if (request.method !== 'POST' || request.url !== '/receipt' && request.url !== '/context') {response.writeHead(404).end('{}'); return;}
      let body = '';
      request.setEncoding('utf8');
      request.on('data', (chunk: string) => {
        body += chunk;
        if (Buffer.byteLength(body) > 16 * 1024) request.destroy();
      });
      request.on('end', async () => {
        try {
          if (request.url === '/context') {
            const receipt = JSON.parse(body) as NativeContextBoundary;
            if (owner.closed || ![receipt.id, receipt.sessionID, receipt.agentID].every(value => typeof value === 'string' && value.length > 0) ||
                receipt.afterUUID !== undefined && (typeof receipt.afterUUID !== 'string' || !receipt.afterUUID)) {response.writeHead(409).end('{}'); return;}
            owner.boundaries.set(receipt.id, receipt);
            await owner.flushBoundaries();
            response.end('{}');
            return;
          }
          const receipt = JSON.parse(body) as {id: string; isDelivered: boolean; reason?: string};
          const pending = owner.pending.get(receipt.id);
          if (!pending || typeof receipt.isDelivered !== 'boolean') {response.writeHead(409).end('{}'); return;}
          owner.pending.delete(receipt.id);
          clearTimeout(pending.timer);
          pending.finish({...pending.command, kind: 'agent_message', failed: !receipt.isDelivered,
            text: receipt.isDelivered ? pending.command.text : String(receipt.reason ?? 'Native message refused')});
          response.end('{}');
        } catch {response.writeHead(400).end('{}');}
      });
    });
    try {
      await mkdir(join(plugin, '.claude-plugin'));
      await mkdir(join(plugin, 'hooks'));
      await writeFile(join(plugin, '.claude-plugin/plugin.json'), JSON.stringify({name: 'mekugi-user-messages', version: '0.1.0', description: 'User-directed native child messages'}), {mode: 0o600});
      await writeFile(join(plugin, 'hooks/hooks.json'), JSON.stringify({modules: ['./register.js']}), {mode: 0o600});
      await writeFile(join(plugin, 'hooks/register.js'), `export function register(on) {
  const lastToolRow = new Map();
  on('session.start', async ($, event, next) => {
    $.clock.every(250, async () => {
      const options = ${JSON.stringify({socketPath: socket, headers: {authorization: `Bearer ${token}`}})};
      const response = await $.http.fetch('http://localhost/intent', options);
      if (!response.ok) return;
      const intent = JSON.parse(response.text);
      if (!intent.id) return;
      let receipt;
      try {
        const live = (await $.agent.list()).some(agent => agent.id === intent.agentID);
        receipt = live || intent.savedOwned
          ? await $.session.send({to: {agentId: intent.agentID}, text: intent.text})
          : {isDelivered: false, reason: 'Native child does not belong to this session'};
      }
      catch (error) { receipt = {isDelivered: false, reason: String(error)}; }
      await $.http.fetch('http://localhost/receipt', {...options, method: 'POST', body: JSON.stringify({id: intent.id, ...receipt})});
    });
    return next(event);
  });
  on('session.append', async ($, event, next) => {
    if (!event.agentId) return next(event);
    const result = await next(event);
    if ((result.message.type === 'assistant' || result.message.type === 'user') && result.message.content.some(block => block.type === 'tool_use' || block.type === 'tool_result')) {
      lastToolRow.set(event.agentId, result.uuid);
    }
    if (event.origin.kind === 'engine' && event.door === 'compaction' && result.message.name === 'compact_boundary' && result.uuid === event.uuid) {
      try {
        await $.http.fetch('http://localhost/context', {...${JSON.stringify({socketPath: socket, headers: {authorization: `Bearer ${token}`}})}, method: 'POST',
          body: JSON.stringify({id: result.uuid, sessionID: await $.session.id(), agentID: event.agentId, afterUUID: lastToolRow.get(event.agentId)})});
      } catch {} // Observation must preserve the stored native boundary.
    }
    return result;
  });
}
`, {mode: 0o600});
      await new Promise<void>((resolve, reject) => {server.once('error', reject); server.listen(socket, () => {server.off('error', reject); resolve();});});
      return owner;
    } catch (error) {await owner.close(); throw error;}
  }

  // The native append can outrun SDK forwarding. Its exact preceding tool-row
  // UUID orders the boundary after buffered loads, without a grace period.
  async event(event: SDKMessage): Promise<void> {
    if (this.closed) return;
    if ((event.type === 'assistant' || event.type === 'user') && Array.isArray(event.message.content) &&
        event.uuid && event.message.content.some(block => block.type === 'tool_use' || block.type === 'tool_result')) {
      this.drained.set(event.parent_tool_use_id ?? '', event.uuid);
      await this.flushBoundaries();
    }
  }

  private async flushBoundaries(): Promise<void> {
    for (const [id, receipt] of this.boundaries) {
      if (receipt.afterUUID && !Array.from(this.drained.values()).includes(receipt.afterUUID)) continue;
      this.boundaries.delete(id);
      if (!this.closed) await this.contextBoundary(receipt);
    }
  }

  async send(command: AgentMessage, cwd: string, activeSession: string): Promise<AgentMessageReceipt> {
    const refuse = (text: string): AgentMessageReceipt => ({...command, kind: 'agent_message', failed: true, text});
    if (this.closed || !activeSession || command.sessionID !== activeSession) return refuse('Native session changed; message not sent');
    if (!command.id || !command.agentID || !command.text.trim()) return refuse('Invalid native child message');
    if (this.pending.size >= 16 || this.pending.has(command.id)) return refuse('Native child message already pending or queue full');
    // A displayed task ID is only a selector. Saved transcript metadata or the
    // mod's live native roster must establish a child of this native parent.
    const children = await listSubagents(activeSession, {dir: cwd});
    if (this.closed) return refuse('Native query closed; message not sent');
    if (this.pending.size >= 16 || this.pending.has(command.id)) return refuse('Native child message already pending or queue full');
    return new Promise(resolve => {
      const timer = setTimeout(() => {
        this.pending.delete(command.id);
        const queued = this.queued.indexOf(command.id);
        if (queued >= 0) this.queued.splice(queued, 1);
        resolve(refuse('Native delivery was not confirmed; do not resend without checking the child'));
      }, 10000);
      this.pending.set(command.id, {command, savedOwned: children.includes(command.agentID), timer, finish: resolve});
      this.queued.push(command.id);
    });
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    for (const pending of this.pending.values()) {
      clearTimeout(pending.timer);
      pending.finish({...pending.command, kind: 'agent_message', failed: true, text: 'Native query closed before delivery was confirmed'});
    }
    this.pending.clear();
    this.boundaries.clear();
    this.drained.clear();
    this.queued.length = 0;
    this.server.closeAllConnections();
    if (this.server.listening) await new Promise<void>(resolve => {this.server.close(() => resolve());});
    await rm(this.plugin, {recursive: true, force: true});
  }
}
