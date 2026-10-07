import type {Query, Settings} from '@anthropic-ai/claude-agent-sdk';
import {mkdtemp, readFile, rm, unlink} from 'node:fs/promises';
import {join, isAbsolute} from 'node:path';
import {tmpdir} from 'node:os';
import {createHash} from 'node:crypto';
import {setTimeout as delay} from 'node:timers/promises';

interface HookRow {
  event: string; matcher: string; source: string; type?: string;
  commandText?: string; disabled?: boolean; runsInBackground?: boolean; runsOnce?: boolean; condition?: string;
}
interface HooksListing {
  policy: {allDisabled: boolean; managedOnly: boolean; pluginOnly: boolean; policyUnreadable?: boolean; policyHookCount: number};
  hooks: HookRow[];
  errors?: unknown[];
}
const environmentEvents = new Set(['Setup', 'SessionStart', 'CwdChanged', 'FileChanged']);

// The SDK implements the official native /hooks control without declaring it.
// Flag hooks augment caller arrays. Only this invocation's exact owned rows
// are admitted; a background hook can also mutate the session environment.
export async function verifyVCSGuardHooks(query: Query, commandText: string): Promise<void> {
  const native = query as Query & {getHooksListing?: () => Promise<HooksListing>; getSettings?: () => Promise<{effective: Settings; errors?: unknown[]}>};
  if (typeof native.getHooksListing !== 'function' || typeof native.getSettings !== 'function') throw new Error('Native hook inspection unavailable; use --vcs-guard=false to opt out');
  const settings = await native.getSettings();
  if (!settings.effective || settings.errors?.length) throw new Error('Native settings inspection unavailable');
  if (settings.effective.env?.CLAUDE_ENV_FILE) throw new Error('Unowned native session environment; use --vcs-guard=false to opt out');
  if (settings.effective.sandbox?.enabled || settings.effective.sandbox?.credentials?.envVars?.some(rule => ['BASH_ENV', 'MEKUGI_EXEC_TRACK', 'SHELL', 'CLAUDE_CODE_SHELL', 'CLAUDE_ENV_FILE', 'CLAUDE_CODE_SHELL_PREFIX'].includes(rule.name))) {
    throw new Error('Native sandbox startup guarding is not verified; use --vcs-guard=false to opt out');
  }
  const listing = await native.getHooksListing();
  const policy = listing.policy;
  if (!policy || !Array.isArray(listing.hooks) || policy.allDisabled !== false || policy.managedOnly !== false || policy.pluginOnly !== false || policy.policyUnreadable || policy.policyHookCount !== 0 || listing.errors?.length) {
    throw new Error('Native hook policy prevents verified VCS instrumentation; use --vcs-guard=false to opt out');
  }
  const owned = new Set<string>();
  for (const hook of listing.hooks) {
    if (hook.disabled) continue;
    if (hook.source === 'flagSettings' && hook.type === 'command' && hook.commandText === commandText &&
      !hook.runsInBackground && !hook.runsOnce && !hook.condition &&
      (hook.event === 'SessionStart' && !hook.matcher || hook.event === 'PreToolUse' && hook.matcher === '^Bash$')) {
      if (owned.has(hook.event)) throw new Error('Duplicate native startup probe');
      owned.add(hook.event);
      continue;
    }
    if (environmentEvents.has(hook.event)) {
      throw new Error(`Competing native environment hook (${hook.source}); use --vcs-guard=false to opt out`);
    }
    if (hook.event === 'PreToolUse' && (!hook.matcher || new RegExp(hook.matcher).test('Bash'))) {
      throw new Error(`Competing native Bash PreToolUse hook (${hook.source}); use --vcs-guard=false to opt out`);
    }
  }
  if (owned.size !== 2) throw new Error('Native startup probe unavailable; use --vcs-guard=false to opt out');
}

interface NativeVCSGuard {
  settings: Settings;
  verify(query: Query, id?: string): Promise<void>;
  close(): Promise<void>;
}
export async function nativeVCSGuard(helper: string | undefined, startup: string | undefined): Promise<NativeVCSGuard> {
  if (!helper || !startup || !isAbsolute(helper) || !isAbsolute(startup)) throw new Error('Native startup probe executable unavailable');
  if (process.env.CLAUDE_ENV_FILE) throw new Error('Unowned native session environment; use --vcs-guard=false to opt out');
  const directory = await mkdtemp(join(tmpdir(), 'mekugi-native-guard-'));
  const args = ['--claude-guard-env', startup, directory];
  const commandText = [helper, ...args].join(' ');
  const hook = {type: 'command' as const, command: helper, args, timeout: 5};
  const settings: Settings = {hooks: {
    SessionStart: [{hooks: [hook]}], PreToolUse: [{matcher: '^Bash$', hooks: [hook]}],
  }};
  return {
    settings,
    async verify(query: Query, id?: string): Promise<void> {
      await verifyVCSGuardHooks(query, commandText);
      const path = join(directory, id ? createHash('sha256').update(id).digest('hex') : 'startup');
      const deadline = Date.now() + 4000;
      // Native command hooks and SDK callbacks run concurrently. Neither the
      // initialization reply nor hook listing proves completion of SessionStart.
      while (true) {
        try {
          const receipt = await readFile(path, 'utf8');
          if (id) await unlink(path);
          if (receipt.startsWith('deny: ')) throw new Error(`${receipt}; use --vcs-guard=false to opt out`);
          if (receipt !== 'ok') throw new Error('Invalid native startup receipt');
          return;
        } catch (error) {
          if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
          if (Date.now() >= deadline) throw new Error('Native startup environment was not confirmed; use --vcs-guard=false to opt out');
          await delay(20);
        }
      }
    },
    close: () => rm(directory, {recursive: true, force: true}),
  };
}
