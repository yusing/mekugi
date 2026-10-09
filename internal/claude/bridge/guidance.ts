import { readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import type { CompanionConfig } from './companion_transport.js';
import type {Query, Settings} from '@anthropic-ai/claude-agent-sdk';

// These official read-only controls exist in the pinned SDK implementation,
// but are omitted from its public Query declaration.
export async function verifyCompanionGuidance(query: Query): Promise<void> {
  const native = query as Query & {
    getSettings?: () => Promise<{sources: {source: string; settings: Settings}[]; errors?: unknown[]}>;
    getHooksListing?: () => Promise<{policy: {allDisabled: boolean; managedOnly: boolean; policyUnreadable?: boolean}; safeMode?: unknown; bareMode?: unknown; errors?: unknown[]}>;
  };
  if (!native.getSettings || !native.getHooksListing) throw new Error('Mandatory companion guidance inspection unavailable');
  const settings = await native.getSettings();
  if (!Array.isArray(settings.sources) || settings.errors?.length) throw new Error('Mandatory companion guidance unavailable: native settings unreadable');
  // Native sec-default reads policy only. User/project/flag options cannot
  // activate this restriction; managed prependPlugins can unseat the default.
  const policy = settings.sources.find(row => row.source === 'policySettings')?.settings;
  const securityPlugin = 'cc-plugin-sec-default@builtin';
  const options = policy?.pluginConfigs?.[securityPlugin]?.options;
  const managedOnly = options && typeof options === 'object' && 'allowManagedModsOnly' in options ? options.allowManagedModsOnly : undefined;
  if (managedOnly !== undefined && managedOnly !== false &&
      (policy?.prependPlugins === undefined || policy.prependPlugins.some(id => {
        const normalized = id.normalize('NFC').toLowerCase();
        return normalized === securityPlugin || normalized === 'sec-default@builtin';
      }))) {
    throw new Error('Mandatory companion guidance unavailable: native policy allows only managed mods');
  }
  const listing = await native.getHooksListing();
  if (listing.errors?.length || !listing.policy ||
      listing.policy.allDisabled !== false || listing.policy.managedOnly !== false || listing.policy.policyUnreadable ||
      listing.safeMode || listing.bareMode) {
    throw new Error('Mandatory companion guidance unavailable: native settings or policy disable invocation-local mods');
  }
}

// The invocation-local mod reads these same artifacts. Validate before starting
// native execution so missing mandatory guidance cannot become an empty prompt.
export function validateCompanionGuidance(config: CompanionConfig): void {
  if (!config.plugin) return;
  for (const name of ['skills/mekugi/SKILL.md', 'skills/mekugi/frontends.md', '.claude-plugin/plugin.json', 'hooks/hooks.json', 'hooks/register.js']) {
    const path = join(config.plugin, name);
    const file = statSync(path);
    if (!file.isFile()) throw new Error(`${name} guidance is not a regular file`);
    if (file.size > 1024 * 1024) throw new Error(`${name} guidance exceeds the registry carrier bound`);
    readFileSync(path, 'utf8');
  }
}
