import type {CanUseTool, PermissionResult} from '@anthropic-ai/claude-agent-sdk';

type PermissionFrame =
  | {kind: 'permission'; id: string; tool: string; input: Record<string, unknown>; toolUseID: string; agentID?: string; description: string}
  | {kind: 'permission_decision'; id: string; toolUseID: string; allow: boolean}
  | {kind: 'permission_cancelled'; id: string};

type PendingPermission = {toolUseID: string; decide(result: PermissionResult): void; cancel(): void; cleanup(): void};

// Callback resolution submits a response to the SDK. Its abort signal can still
// cancel the control request before native tool evidence confirms consumption.
export class PermissionRequests {
  private readonly pending = new Map<string, PendingPermission>();
  private serial = 0;
  constructor(private readonly emit: (frame: PermissionFrame) => Promise<void>, private readonly failed: () => void) {}
  get size(): number {return this.pending.size;}

  readonly canUseTool: CanUseTool = async (tool, input, options) => {
    const id = String(++this.serial);
    return new Promise<PermissionResult>(resolve => {
      let submitted = false;
      const cleanup = (): void => {
        this.pending.delete(id);
        options.signal.removeEventListener('abort', cancelled);
      };
      const cancelled = (): void => {
        cleanup();
        resolve({behavior: 'deny', message: 'Native permission request cancelled'});
        void this.emit({kind: 'permission_cancelled', id}).catch(this.failed);
      };
      this.pending.set(id, {toolUseID: options.toolUseID, cleanup, cancel: cancelled, decide: result => {
        if (submitted) return;
        submitted = true;
        resolve(result.behavior === 'allow' && result.updatedInput ? {...result, updatedInput: {...input, ...result.updatedInput}} : result);
        void this.emit({kind: 'permission_decision', id, toolUseID: options.toolUseID, allow: result.behavior === 'allow'}).catch(this.failed);
      }});
      options.signal.addEventListener('abort', cancelled, {once: true});
      if (options.signal.aborted) {cancelled(); return;}
      void this.emit({kind: 'permission', id, tool, input, toolUseID: options.toolUseID,
        ...(options.agentID ? {agentID: options.agentID} : {}), description: options.description ?? options.title ?? ''}).catch(this.failed);
    });
  };

  respond(id: string, result: PermissionResult): void {this.pending.get(id)?.decide(result);}
  settle(toolUseID: string): void {
    for (const permission of this.pending.values()) {
      if (permission.toolUseID === toolUseID) permission.cleanup();
    }
  }
  close(): void {for (const permission of this.pending.values()) permission.cancel();}
}
