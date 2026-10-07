import type { Query, SDKMessage } from '@anthropic-ai/claude-agent-sdk';

interface Tail {output: string; total_bytes: number; truncated: boolean}
interface NativeOutput {getTaskOutput(taskID: string): Promise<Tail>}
interface Task {id: string; tool: string; caller: string; background: boolean; terminal: boolean; read?: Promise<void>; tail?: Tail}
export interface OutputFrame {
  kind: 'command_output'; id: string; caller: string; taskID: string;
  text: string; truncated: boolean; done: boolean; failed: boolean;
}

// SDK 0.3.288 implements getTaskOutput but does not expose it on Query's public
// declaration. This adapter is read-only; output failures never settle tools.
export function taskOutput(query: Query, emit: (frame: OutputFrame) => Promise<void>, notice: (text: string) => Promise<void>): {
  event(event: SDKMessage): Promise<void>; close(): void;
} {
  const native = query as unknown as NativeOutput;
  const tasks = new Map<string, Task>();
  let timer: ReturnType<typeof setTimeout> | undefined;
  let closed = false;
  let warned = false;
  let cursor = 0;
  const warn = (error: unknown): void => {
    if (closed || warned) return;
    warned = true;
    void notice(`Native command output unavailable: ${String(error)}`).catch(() => {});
  };
  const sample = (task: Task, done = false, failed = false): Promise<void> => {
    if (closed) return Promise.resolve();
    if (task.read) return task.read;
    task.read = Promise.resolve().then(async () => {
    try {
      const tail = await native.getTaskOutput(task.id);
      if (closed || tasks.get(task.id) !== task) return;
      if (typeof tail.output !== 'string' || Buffer.byteLength(tail.output) > 16 * 1024 || !Number.isSafeInteger(tail.total_bytes) || tail.total_bytes < 0 || typeof tail.truncated !== 'boolean') {
        throw new Error('Invalid native command output snapshot');
      }
      if (!done && task.tail?.output === tail.output && task.tail.truncated === tail.truncated) return;
      task.tail = tail;
      await emit({kind: 'command_output', id: task.tool, caller: task.caller, taskID: task.id,
        text: tail.output, truncated: tail.truncated, done, failed});
    } catch (error) { if (tasks.get(task.id) === task) warn(error); }
    finally {task.read = undefined;}
    });
    return task.read;
  };
  const schedule = (): void => {
    if (closed || timer || tasks.size === 0) return;
    timer = setTimeout(() => {
      timer = undefined;
      // A hung native read holds only its own slot. Do not build an unbounded
      // control queue or block native event delivery behind presentation reads.
      const available = [...tasks.values()];
      const free = 4 - available.filter(task => task.read).length;
      let admitted = 0;
      for (let examined = 0; examined < available.length && admitted < free; examined++) {
        const task = available[cursor++ % available.length]!;
        if (!task.read && !task.terminal) {admitted++; void sample(task);}
      }
      schedule();
    }, 125);
  };
  const track = (id: string, tool: string, caller = '', background = false): void => {
    if (!id || !tool || closed) return;
    const old = tasks.get(id);
    if (old) {
      if (old.tool !== tool) {warn(new Error('Native command output identity changed')); return;}
      old.background ||= background;
      return;
    }
    if (tasks.size >= 256) {warn(new Error('Native command output task limit reached')); return;}
    tasks.set(id, {id, tool, caller, background, terminal: false});
    schedule();
  };
  const finish = async (task: Task, failed: boolean): Promise<void> => {
    // Reconcile a pending read before the final snapshot. Background tasks have
    // no later aggregate; never discard readable final bytes at their edge.
    // One deadline bounds both reads independently of native tool settlement.
    task.terminal = true;
    let deadline: ReturnType<typeof setTimeout> | undefined;
    await Promise.race([(task.read ?? Promise.resolve()).then(() => {
      if (!closed && tasks.get(task.id) === task) return sample(task, task.background, failed);
    }),
      new Promise<void>(resolve => {deadline = setTimeout(resolve, 750);})]);
    clearTimeout(deadline);
    tasks.delete(task.id);
  };
  return {
    close: () => {closed = true; clearTimeout(timer); tasks.clear();},
    event: async event => {
      if (closed) return;
      if (event.type === 'tool_progress' && event.tool_name === 'Bash' && event.task_id) {
        track(event.task_id, event.tool_use_id, event.parent_tool_use_id ?? '');
      } else if (event.type === 'system') {
        if (event.subtype === 'task_started' && event.task_type === 'local_bash' && event.tool_use_id && !event.ambient && !event.skip_transcript) {
          track(event.task_id, event.tool_use_id, '', event.is_backgrounded ?? false);
        } else if (event.subtype === 'task_updated') {
          const task = tasks.get(event.task_id);
          if (!task) return;
          task.background ||= event.patch.is_backgrounded ?? false;
          if (['completed', 'failed', 'killed'].includes(event.patch.status ?? '')) await finish(task, event.patch.status === 'failed');
        } else if (event.subtype === 'task_notification') {
          const task = tasks.get(event.task_id);
          if (task && (!event.tool_use_id || event.tool_use_id === task.tool)) await finish(task, event.status === 'failed');
        }
      } else if (event.type === 'user' && Array.isArray(event.message.content)) {
        for (const block of event.message.content) {
          if (block.type !== 'tool_result') continue;
          for (const task of tasks.values()) if (task.tool === block.tool_use_id && !task.background) await finish(task, block.is_error ?? false);
        }
      }
    },
  };
}
