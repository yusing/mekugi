import { request } from 'node:http';

export interface CompanionConfig {
  socket: string; token: string; plugin?: string; frontendDirectory?: string; bashEnv?: string;
  journalSchema?: Record<string, unknown>;
  managedSkills?: boolean;
  vcsGuard?: boolean;
  vcsGuardHelper?: string;
}

export function companionRequest(config: CompanionConfig, payload: unknown, signal?: AbortSignal, responseLimit = 8192): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(payload);
    if (Buffer.byteLength(data) > 8 * 1024 * 1024) { reject(new Error('observation exceeds frame limit')); return; }
    const req = request({socketPath: config.socket, path: '/observe', method: 'POST', signal,
      headers: {'Authorization': `Bearer ${config.token}`, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data)}}, response => {
      let bytes = 0; const chunks: Buffer[] = [];
      response.on('data', (chunk: Buffer) => {
        bytes += chunk.length;
        if (bytes > responseLimit) {
          const error = new Error('oversized observation response');
          reject(error); req.destroy(error);
        } else chunks.push(chunk);
      });
      response.on('end', () => {
        const message = Buffer.concat(chunks).toString();
        if (response.statusCode !== 200) { reject(new Error(`observation rejected (${response.statusCode}): ${message.trim()}`)); return; }
        try { resolve(JSON.parse(message)); } catch { reject(new Error('invalid companion response')); }
      });
      response.on('error', reject);
    });
    req.setTimeout(4000, () => req.destroy(new Error('observation timeout')));
    req.on('error', reject); req.end(data);
  });
}
