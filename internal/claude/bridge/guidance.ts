import { readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import type { CompanionConfig } from './companion_transport.js';

export interface CompanionPrompt {workflow: string; frontends: string[]}

// Native Claude persists a hook response over 10,000 UTF-16 units and exposes
// only a short preview. Workflow and tool contracts use separate bounded hooks.
export function companionGuidance(config: CompanionConfig): string {
  if (!config.plugin) return '';
  const directory = join(config.plugin, 'skills', 'mekugi');
  const skill = readFileSync(join(directory, 'SKILL.md'), 'utf8');
  const body = skill.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n/, '').trim();
  const frontends = join(directory, 'frontends.md');
  if (!statSync(frontends).isFile()) throw new Error('Frontend contracts are not a regular file');
  const guidance = `${body}\n\nAuthenticated frontend contracts are injected into native context. If that context is unavailable during recovery, read the reference at ${JSON.stringify(frontends)}.`;
  if (guidance.length > 10000) throw new Error('Mandatory companion guidance exceeds native inline context capacity');
  return guidance;
}

export function companionFrontendGuidance(config: CompanionConfig): string[] {
  if (!config.plugin) return [];
  const path = join(config.plugin, 'skills', 'mekugi', 'frontends.md');
  if (statSync(path).size > 1024 * 1024) throw new Error('Frontend guidance exceeds the registry carrier bound');
  const contracts = readFileSync(path, 'utf8');
  const chunks: string[] = [];
  let remaining = contracts;
  while (remaining) {
    let end = Math.min(9000, remaining.length);
    if (end < remaining.length) {
      const boundary = remaining.lastIndexOf('\n## ', end);
      if (boundary > 0) end = boundary + 1;
      else {
        const newline = remaining.lastIndexOf('\n', end);
        if (newline > 0) end = newline + 1;
        else if (/[\uD800-\uDBFF]/.test(remaining[end - 1]!)) end--;
      }
    }
    chunks.push(remaining.slice(0, end));
    remaining = remaining.slice(end);
  }
  return chunks;
}
