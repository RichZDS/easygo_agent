import type { Block, Run, Tool } from '../types.js';
import { fields, RpcError } from '../validation.js';

type Role = 'assistant' | 'foreman';
export interface ToolEntry {
  definition: Tool;
  roles: readonly Role[];
  mutating: boolean;
  execute(call: Block, run: Run, signal: AbortSignal): Promise<unknown>;
  recoverable(error: unknown): boolean;
}

export class ToolRegistry {
  private entries = new Map<string, ToolEntry>();
  constructor(entries: ToolEntry[]) {
    for (const entry of entries) {
      if (this.entries.has(entry.definition.name)) throw new Error(`Duplicate tool: ${entry.definition.name}`);
      this.entries.set(entry.definition.name, entry);
    }
  }
  definitions(role: Role): Tool[] {
    return [...this.entries.values()].filter((e) => e.roles.includes(role)).map((e) => e.definition);
  }
  private entry(role: Role, name?: string): ToolEntry | undefined {
    const entry = this.entries.get(name ?? '');
    return entry?.roles.includes(role) ? entry : undefined;
  }
  async execute(role: Role, call: Block, run: Run, signal: AbortSignal): Promise<unknown> {
    signal.throwIfAborted();
    if (Buffer.byteLength(JSON.stringify(call.arguments) ?? '') > 65536)
      throw new RpcError(-32602, 'tool_arguments_too_large');
    const entry = this.entry(role, call.name);
    if (!entry) throw new RpcError(-32602, 'unknown_tool');
    const schema = entry.definition.parameters as { properties: Record<string, unknown> };
    fields(call.arguments, Object.keys(schema.properties));
    return entry.execute(call, run, signal);
  }
  recoverable(role: Role, name: string | undefined, error: unknown): boolean {
    const entry = this.entry(role, name);
    return entry ? entry.recoverable(error) : error instanceof RpcError && error.code === -32602;
  }
}
