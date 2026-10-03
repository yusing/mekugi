import { createSdkMcpServer, tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { companionRequest, type CompanionConfig } from './companion_transport.js';

export function nativeToolUseID(extra: unknown): string | undefined {
  if (!extra || typeof extra !== 'object') return undefined;
  const meta = (extra as {_meta?: unknown})._meta;
  if (!meta || typeof meta !== 'object') return undefined;
  const id = (meta as Record<string, unknown>)['claudecode/toolUseId'];
  return typeof id === 'string' && id.length > 0 && id.length <= 1024 ? id : undefined;
}

export function journalServer(config: CompanionConfig): ReturnType<typeof createSdkMcpServer> | undefined {
  if (!config.journalSchema) return undefined;
  // JSON Schema is supplied by the shared journal owner, not a Claude catalog.
  const journal = z.fromJSONSchema(config.journalSchema);
  const invoke = async (operation: 'journal_batch' | 'journal_read' | 'mchanges', args: unknown, extra: unknown) => {
    try {
      const nativeID = nativeToolUseID(extra);
      if (!nativeID) throw new Error('Native MCP caller identity unavailable; journal not modified');
      const result = await companionRequest(config, {operation, nativeID, input: JSON.stringify(args)}, undefined, 4 * 1024 * 1024);
      return {content: [{type: 'text' as const, text: JSON.stringify(result)}]};
    } catch (error) { return {isError: true, content: [{type: 'text' as const, text: String(error)}]}; }
  };
  return createSdkMcpServer({name: 'mekugi', version: '1.0.0', tools: [
    tool('mchanges', 'Read captured changes using shared mchanges arguments. Empty args, --mine and --list select only your own recorded changes. Supports explicit IDs, --net, --summary, paths and bounded output. Read-only: apply/revert stay in native Bash with explicit IDs.', {args: z.array(z.string())},
      (args, extra) => invoke('mchanges', args, extra)),
    tool('journal_batch', 'Atomically mutate your durable journal. Stable ordinal paths are returned. Use reset: slice on a plan for journal-only context reset between slices. Mark completed tasks done and input-needed tasks blocked: the UI continues runnable tasks after successful native turns. An answer claiming completion does not change task state.', {journal},
      (args, extra) => invoke('journal_batch', args, extra)),
    tool('journal_read', 'Read your durable journal tree. Other agents require proven ancestry; reads never acknowledge terminal delivery.', {
      p: z.string().optional(), agent: z.string().optional(), depth: z.number().int().nonnegative().optional(),
      view: z.enum(['combined', 'own', 'tasks', 'outline']).optional(),
    }, (args, extra) => invoke('journal_read', args, extra)),
  ]});
}
