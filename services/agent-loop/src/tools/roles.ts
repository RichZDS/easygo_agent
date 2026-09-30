import { openSync, readSync, closeSync, fstatSync } from 'node:fs';
import { join } from 'node:path';
import type { Config } from '../types.js';

export function systemPrompt(config: Pick<Config, 'pack_dir' | 'system_prompt'>): string | undefined {
  if (config.pack_dir === undefined) return config.system_prompt;
  if (config.system_prompt !== undefined) throw new Error('pack_dir conflicts with system_prompt');
  const fd = openSync(join(config.pack_dir, 'roles', 'assistant.md'), 'r');
  try {
    if (!fstatSync(fd).isFile()) throw new Error('assistant role must be a file');
    const bytes = Buffer.alloc(16385);
    let size = 0,
      count: number;
    while (size < bytes.length && (count = readSync(fd, bytes, size, bytes.length - size, null)) > 0) size += count;
    if (size > 16384) throw new Error('assistant role exceeds 16 KiB');
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, size));
  } finally {
    closeSync(fd);
  }
}
