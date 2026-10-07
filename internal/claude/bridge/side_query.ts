import {query, type Query, type SDKUserMessage, type EffortLevel} from '@anthropic-ai/claude-agent-sdk';

type SideInput = {id: string; source: string; content: SDKUserMessage['message']['content']};
type SideOptions = {cwd: string; executable: string; model?: string; effort?: EffortLevel};
type Inbox = {messages: SDKUserMessage[]; wake?: () => void; closed: boolean};
type Side = {query: Query; inbox: Inbox; busy: boolean; pump: Promise<void>};

// A native fork owns context and follow-ups. Only its presentation is multiplexed.
export class SideQueries {
  private sides = new Map<string, Side>();
  constructor(private emit: (frame: unknown) => Promise<void>) {}

  async input(input: SideInput, options: SideOptions): Promise<void> {
    const send = (frame: unknown): Promise<void> => this.emit({kind: 'side', id: input.id, frame});
    try {
      if (!input.id || !input.source || !Array.isArray(input.content)) throw new Error('Invalid side input');
      let side = this.sides.get(input.id);
      if (side?.busy) throw new Error('Side query is already answering');
      if (!side) {
        if (this.sides.size) throw new Error('Close the previous side conversation before opening another');
        // The fork reads the saved native chain at admission, not a router transcript.
        // query() consumes its generator synchronously during construction.
        const inbox: Inbox = {messages: [], closed: false};
        async function* prompt(): AsyncGenerator<SDKUserMessage> {
          while (!inbox.closed) {
            if (inbox.messages.length) yield inbox.messages.shift()!;
            else await new Promise<void>(resolve => {inbox.wake = resolve;});
          }
        }
        const runtime = query({prompt: prompt(), options: {
          cwd: options.cwd, pathToClaudeCodeExecutable: options.executable,
          resume: input.source, forkSession: true, persistSession: false,
          systemPrompt: {type: 'preset', preset: 'claude_code'},
          settingSources: ['user', 'project', 'local'], includePartialMessages: true,
          tools: [], mcpServers: {}, strictMcpConfig: true,
          canUseTool: async () => ({behavior: 'deny', message: 'Side questions cannot use tools'}),
          ...(options.model ? {model: options.model} : {}),
          ...(options.effort ? {effort: options.effort} : {}),
        }});
        side = {query: runtime, inbox, busy: false, pump: Promise.resolve()};
        this.sides.set(input.id, side);
        const owned = side;
        owned.pump = (async () => {
          try {
            for await (const event of runtime) {
              if (inbox.closed) break;
              if (event.type === 'result') owned.busy = false;
              await send({kind: 'event', event});
            }
            if (!inbox.closed) throw new Error('Native side query ended unexpectedly');
          } catch (error) {
            if (!inbox.closed) await send({kind: 'error', text: String(error)});
          } finally {
            inbox.closed = true;
            inbox.wake?.();
            runtime.close();
            if (this.sides.get(input.id) === owned) this.sides.delete(input.id);
            await send({kind: 'side_closed'});
          }
        })();
      }
      side.busy = true;
      side.inbox.messages.push({type: 'user', message: {role: 'user', content: input.content}, parent_tool_use_id: null, origin: {kind: 'human'}});
      side.inbox.wake?.(); side.inbox.wake = undefined;
    } catch (error) { await send({kind: 'error', text: String(error)}); }
  }

  async close(id: string): Promise<void> {
    const side = this.sides.get(id);
    if (!side) return;
    side.inbox.closed = true;
    side.inbox.wake?.();
    side.query.close();
    await side.pump;
  }

  async closeAll(): Promise<void> {
    await Promise.all([...this.sides.keys()].map(id => this.close(id)));
  }
}
