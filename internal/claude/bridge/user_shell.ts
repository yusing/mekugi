import type {SDKMessage, SDKUserMessage} from '@anthropic-ai/claude-agent-sdk';
import {randomUUID} from 'node:crypto';

export interface ShellCommand {id: string; sessionID: string; text: string}
export interface BashInput {type: 'bash_command'; command: string; cwd: string; uuid: ReturnType<typeof randomUUID>; session_id: string}
export interface EndSessionInput {type: 'control_request'; request_id: string; request: {subtype: 'end_session'}}
export type NativeInput = SDKUserMessage | BashInput | EndSessionInput;
export interface ShellFrame {
  kind: 'shell_started' | 'shell_done'; id: string; sessionID: string; command: string;
  output?: string; failed?: boolean; retained?: boolean; text?: string;
}
interface Lifecycle {type: 'command_lifecycle'; command_uuid: string; state: string; session_id: string}
interface Pending {
  command: ShellCommand; uuid: string; session: string; input?: string; output?: string;
  appends: Set<string>; retentionError?: string;
  shutdown?: {resolve: (receipt: CompletedShell) => void; reject: (error: unknown) => void};
}
export interface CompletedShell {command: ShellCommand; session: string; input: string; output: string}
const escape = (text: string): string => text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');

// The native stdin schema admits bash_command; the pinned SDK prompt declaration
// omits it. The sole type extension is at the public query generator boundary.
// Replay receipts are display-only. Public shouldQuery:false appends retain their
// exact native content, and correlated lifecycle completion confirms the append.
export class UserShell {
  private current?: Pending;
  constructor(private readonly push: (input: NativeInput) => void, private readonly emit: (frame: ShellFrame) => Promise<void>) {}
  get pending(): boolean {return Boolean(this.current);}

  // Raw Bash uses native session lifetime, not the model-turn interrupt signal.
  // end_session returns real terminal output but stops the old input reader.
  shutdown(): Promise<CompletedShell> | undefined {
    const p = this.current;
    if (!p || p.appends.size || p.shutdown) return;
    return new Promise((resolve, reject) => {
      p.shutdown = {resolve, reject};
      this.push({type: 'control_request', request_id: randomUUID(), request: {subtype: 'end_session'}});
    });
  }

  queryEnded(error: unknown): void {
    const p = this.current;
    if (!p?.shutdown) return;
    this.current = undefined;
    p.shutdown.reject(error);
  }

  retain(receipt: CompletedShell): void {
    const p: Pending = {...receipt, uuid: '', appends: new Set()};
    this.current = p;
    this.append(p);
  }

  private append(p: Pending): void {
    const caveat = "<local-command-caveat>The command below was run directly in Claude Code, not sent to you as a request, and its output goes straight to the user. It's recorded here as context for later messages.</local-command-caveat>";
    for (const [index, content] of [caveat, p.input!, p.output!].entries()) {
      const uuid = randomUUID();
      p.appends.add(uuid);
      this.push({type: 'user', uuid, session_id: p.session, parent_tool_use_id: null, shouldQuery: false,
        ...(index === 0 ? {isSynthetic: true} : {}), client_composed: true, message: {role: 'user', content}});
    }
  }

  async start(command: ShellCommand, cwd: string, session: string): Promise<void> {
    if (this.current || !command.id || !command.text.trim() || command.sessionID !== session) {
      await this.emit({kind: 'shell_done', id: command.id, sessionID: command.sessionID, command: command.text, failed: true, text: 'Native shell is not ready; command not executed'});
      return;
    }
    const uuid = randomUUID();
    this.current = {command, uuid, session, appends: new Set()};
    this.push({type: 'bash_command', command: command.text, cwd, uuid, session_id: session});
  }

  async event(event: SDKMessage | Lifecycle): Promise<boolean> {
    const p = this.current;
    if (!p) return false;
    if (event.type === 'user' && 'isReplay' in event && event.isReplay && typeof event.message.content === 'string') {
      if (p.appends.has(event.uuid)) return true;
      if (p.session && event.session_id !== p.session) return false;
      const content = event.message.content;
      if (!p.input && content === `<bash-input>${escape(p.command.text)}</bash-input>`) {
        p.input = content;
        p.session = event.session_id;
        await this.emit({kind: 'shell_started', id: p.command.id, sessionID: p.session, command: p.command.text});
        return true;
      }
      if (p.input && !p.output && /^<bash-stdout>[\s\S]*<\/bash-stdout><bash-stderr>[\s\S]*<\/bash-stderr><bash-exit-code>-?\d+<\/bash-exit-code>$/.test(content)) {
        p.output = content;
        return true;
      }
    }
    if (event.type === 'result' && 'user_message_uuid' in event && p.appends.has(event.user_message_uuid ?? '')) {
      if (event.is_error) p.retentionError = 'Native command history append failed';
      return true; // Transcript-only results are not Main turn completion.
    }
    if (event.type !== 'command_lifecycle' || event.state !== 'completed' || event.session_id !== p.session) return false;
    if (p.appends.delete(event.command_uuid)) {
      if (!p.appends.size) {
        this.current = undefined;
        await this.emit({kind: 'shell_done', id: p.command.id, sessionID: p.session, command: p.command.text,
          output: p.output, retained: !p.retentionError, text: p.retentionError});
      }
      return true;
    }
    if (event.command_uuid !== p.uuid) return false;
    // Only a terminal native receipt permits context insertion. No shell effects
    // are repeated, and a missing native receipt cannot become saved evidence.
    if (!p.input || !p.output) {
      if (p.shutdown) throw new Error('Native shutdown did not return shell evidence; resume manually');
      this.current = undefined;
      await this.emit({kind: 'shell_done', id: p.command.id, sessionID: p.session, command: p.command.text,
        failed: true, text: 'Native shell completed without command/output evidence; history unavailable'});
      return true;
    }
    if (p.shutdown) {
      this.current = undefined;
      p.shutdown.resolve({command: p.command, session: p.session, input: p.input, output: p.output});
      return true;
    }
    if (!p.appends.size) {
      this.append(p);
    }
    return true;
  }
}
