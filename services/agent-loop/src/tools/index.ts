import type { Knowledge } from '../knowledge/index.js';
import type { RpcClient } from '../rpc.js';
import { RpcError } from '../validation.js';
import { ToolRegistry, type ToolEntry } from './registry.js';
import { workshopTools } from './workshop.js';

export { ToolRegistry } from './registry.js';
export function createToolRegistry(client: RpcClient, knowledge?: Knowledge): ToolRegistry {
  const entries: ToolEntry[] = workshopTools(client);
  for (const definition of knowledge?.tools() ?? []) entries.push({
    definition, roles: ['assistant'], mutating: false,
    execute: (call, run, signal) => knowledge!.execute(call, run.namespace, signal),
    recoverable: error => error instanceof RpcError && [-32602, -32004, -32009].includes(error.code)
  });
  return new ToolRegistry(entries);
}
