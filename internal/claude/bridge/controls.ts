import type { Query, EffortLevel } from '@anthropic-ai/claude-agent-sdk';

export type SettingsCommand = {kind: 'settings'; id: string; field: 'model' | 'effort'; value: string};
export type SettingsReceipt = SettingsCommand & {failed?: boolean; text?: string};

// Dedicated native controls only. No settings-file writer, hooks or prompt edits.
export async function setSettings(runtime: Pick<Query, 'setModel' | 'applyFlagSettings'>, command: SettingsCommand): Promise<SettingsReceipt> {
  try {
    if (typeof command.id !== 'string' || typeof command.value !== 'string' || command.value.length === 0) throw new Error('Invalid settings control');
    if (command.field === 'model') {
      await runtime.setModel(command.value === 'default' ? undefined : command.value);
    } else if (command.field === 'effort') {
      const value = command.value;
      if (value !== 'default' && !['low', 'medium', 'high', 'xhigh', 'max'].includes(value)) throw new Error('Unsupported effort level');
      await runtime.applyFlagSettings({effortLevel: value === 'default' ? null : value as EffortLevel});
    } else {
      throw new Error('Unsupported settings control');
    }
    return command;
  } catch (error) {
    return {...command, failed: true, text: String(error)};
  }
}
