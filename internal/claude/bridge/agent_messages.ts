import {listSubagents} from '@anthropic-ai/claude-agent-sdk';
import {createServer, type Server} from 'node:http';
import {mkdtemp, mkdir, writeFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {randomUUID} from 'node:crypto';

export interface AgentMessage {id: string; sessionID: string; agentID: string; text: string}
export interface AgentMessageReceipt extends AgentMessage {kind: 'agent_message'; failed?: boolean}
interface Pending {command: AgentMessage; savedOwned: boolean; finish: (receipt: AgentMessageReceipt) => void; timer: ReturnType<typeof setTimeout>}

// SDK 0.3.288 has no supported direct-message Query control. Claude Code
// 2.1.288's official invocation-local mod API calls native session.send instead.
// The engine stamps plugin provenance and owns continuation and permissions.
export class AgentMessages {
  private readonly pending = new Map<string, Pending>();
  private readonly queued: string[] = [];
  private closed = false;
  private constructor(readonly plugin: string, private readonly server: Server) {}

  static async create(): Promise<AgentMessages> {
    const plugin = await mkdtemp(join(tmpdir(), 'mekugi-agent-'));
    const socket = join(plugin, 'control.sock');
    const token = randomUUID();
    const server = createServer();
    const owner = new AgentMessages(plugin, server);
    server.on('request', (request, response) => {
      response.setHeader('Content-Type', 'application/json');
      if (request.headers.authorization !== `Bearer ${token}`) {response.writeHead(403).end('{}'); return;}
      if (request.method === 'GET' && request.url === '/intent') {
        let pending: Pending | undefined;
        while (owner.queued.length && !pending) pending = owner.pending.get(owner.queued.shift()!);
        response.end(JSON.stringify(pending ? {...pending.command, savedOwned: pending.savedOwned} : {}));
        return;
      }
      if (request.method !== 'POST' || request.url !== '/receipt') {response.writeHead(404).end('{}'); return;}
      let body = '';
      request.setEncoding('utf8');
      request.on('data', (chunk: string) => {
        body += chunk;
        if (Buffer.byteLength(body) > 16 * 1024) request.destroy();
      });
      request.on('end', () => {
        try {
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
}
`, {mode: 0o600});
      await new Promise<void>((resolve, reject) => {server.once('error', reject); server.listen(socket, () => {server.off('error', reject); resolve();});});
      return owner;
    } catch (error) {await owner.close(); throw error;}
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
    this.queued.length = 0;
    this.server.closeAllConnections();
    if (this.server.listening) await new Promise<void>(resolve => {this.server.close(() => resolve());});
    await rm(this.plugin, {recursive: true, force: true});
  }
}
