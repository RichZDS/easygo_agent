import type { Block, Run, Tool } from './types.js';
import { RpcClient } from './rpc.js';
import { fields, integer, object, RpcError, string } from './validation.js';

const short = { type: 'string', minLength: 1, maxLength: 128 };
const input = { type: 'string', minLength: 1, maxLength: 32768 };
const offset = { type: 'integer', minimum: 0, maximum: 2147483647 };
function definition(name: string, description: string, properties: Record<string, unknown>, required: string[]): Tool {
  return { name, description, parameters: { type: 'object', properties, required, additionalProperties: false } };
}
export const TOOLS: Tool[] = [
  definition('calculator', 'Perform deterministic arithmetic.', { operation: { enum: ['add', 'subtract', 'multiply', 'divide'], type: 'string' }, a: { type: 'number' }, b: { type: 'number' } }, ['operation', 'a', 'b']),
  definition('workshop_catalog', 'List configured workflows.', {}, []),
  definition('workshop_submit', 'Submit a workflow; acceptance is not completion. Read task status afterwards.', { workflow: short, input, runtime: short }, ['workflow', 'input']),
  definition('workshop_get', 'Read a bounded task summary.', { task_id: short }, ['task_id']),
  definition('workshop_list', 'List tasks in the current namespace.', { offset, limit: { type: 'integer', minimum: 1, maximum: 100 } }, []),
  definition('workshop_cancel', 'Cancel a task.', { task_id: short }, ['task_id']),
  definition('workshop_resume', 'Resume a task with input.', { task_id: short, input }, ['task_id', 'input']),
  definition('workshop_result', 'Read a bounded result page.', { task_id: short, run_id: short, offset, limit: { type: 'integer', minimum: 4, maximum: 32768 } }, ['task_id'])
];
export async function executeTool(call: Block, run: Run, client: RpcClient, signal: AbortSignal): Promise<unknown> {
  signal.throwIfAborted();
  if (Buffer.byteLength(JSON.stringify(call.arguments) ?? '') > 65536) throw new RpcError(-32602, 'tool_arguments_too_large');
  const definition = TOOLS.find(t => t.name === call.name);
  if (!definition) throw new RpcError(-32602, 'unknown_tool');
  const schema = definition.parameters as { properties: Record<string, unknown> };
  const args = fields(call.arguments, Object.keys(schema.properties));
  if (call.name === 'calculator') {
    const a = args.a, b = args.b;
    if (typeof a !== 'number' || typeof b !== 'number' || !Number.isFinite(a) || !Number.isFinite(b)) throw new RpcError(-32602, 'invalid_operands');
    const operation = string(args.operation);
    let result: number;
    switch (operation) {
      case 'add': result = a + b; break;
      case 'subtract': result = a - b; break;
      case 'multiply': result = a * b; break;
      case 'divide': if (b === 0) throw new RpcError(-32602, 'division_by_zero'); result = a / b; break;
      default: throw new RpcError(-32602, 'invalid_operation');
    }
    if (!Number.isFinite(result)) throw new RpcError(-32602, 'arithmetic_overflow');
    return { result };
  }
  const p: Record<string, unknown> = { namespace: run.namespace };
  switch (call.name) {
    case 'workshop_catalog': return callWorkshop(client, 'workshop.workflows', p, signal);
    case 'workshop_submit':
      p.workflow = string(args.workflow); p.input = string(args.input, 32768);
      if (run.workshop_runtime) p.runtime = run.workshop_runtime;
      else if (args.runtime !== undefined) p.runtime = string(args.runtime);
      // Trusted, deterministic identity. Tool-call IDs are validated unique per run.
      p.idempotency_key = `${run.id}:${string(call.id, 128)}`;
      break;
    case 'workshop_resume': p.task_id = string(args.task_id); p.input = string(args.input, 32768); break;
    case 'workshop_result':
      p.task_id = string(args.task_id); if (args.run_id !== undefined) p.run_id = string(args.run_id);
      p.offset = integer(args.offset, 0, 2147483647); p.limit = integer(args.limit, 8192, 32768, 4); break;
    case 'workshop_list': p.offset = integer(args.offset, 0, 2147483647); p.limit = integer(args.limit, 20, 100, 1); break;
    default: p.task_id = string(args.task_id);
  }
  return callWorkshop(client, `workshop.${call.name!.slice('workshop_'.length)}`, p, signal);
}

class ToolRpcFailure extends RpcError {
  constructor(reason: string, readonly recoverable: boolean, upstream?: unknown) {
    super(-32000, reason, 'Workshop RPC failed', upstream);
  }
}
export function recoverableToolError(error: unknown): boolean {
  // Only locally generated argument/calculator errors reach this branch directly.
  // RPC parsing errors are wrapped below, so malformed upstream responses cannot
  // masquerade as a safe local validation rejection of a mutating operation.
  return error instanceof ToolRpcFailure ? error.recoverable : error instanceof RpcError && error.code === -32602;
}
async function callWorkshop(client: RpcClient, method: string, params: Record<string, unknown>, signal: AbortSignal): Promise<unknown> {
  try {
    const result = await client.call(method, params, signal);
    validateReceipt(method, params, result);
    return result;
  }
  catch (error) {
    signal.throwIfAborted();
    // A request deadline terminates the run, including otherwise retry-safe reads.
    if (error instanceof Error && ['TimeoutError', 'AbortError'].includes(error.name)) throw error;
    const upstream = error instanceof RpcError && error.reason === 'upstream_error' ? error.details as { code: number } : undefined;
    const mutating = ['workshop.submit', 'workshop.resume', 'workshop.cancel'].includes(method);
    // Only explicit pre-acceptance rejections allow the model another attempt.
    // Internal/execution/protocol/transport failures can follow acceptance; never
    // hand those mutating outcomes back to a model that could submit again.
    const rejected = upstream !== undefined && [-32602, -32004, -32003, -32009, -32029].includes(upstream.code);
    const recoverable = !mutating || rejected;
    throw new ToolRpcFailure(recoverable ? 'upstream_error' : 'uncertain_tool_outcome', recoverable, upstream);
  }
}

// Check trusted identity before exposing remote task data to the model. A valid
// JSON success envelope alone does not prove that a mutation was acknowledged.
function validateReceipt(method: string, params: Record<string, unknown>, value: unknown): void {
  const task = (value: unknown, requestedID?: unknown) => {
    const receipt = object(value);
    const id = string(receipt.id);
    if (receipt.namespace !== params.namespace || (requestedID !== undefined && id !== requestedID) ||
        !['queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled', 'timed_out', 'interrupted'].includes(string(receipt.status))) {
      throw new Error('Invalid workshop receipt');
    }
  };
  if (['workshop.submit', 'workshop.get', 'workshop.cancel', 'workshop.resume'].includes(method)) {
    task(value, params.task_id);
  } else if (method === 'workshop.list') {
    const page = object(value);
    if (!Array.isArray(page.tasks) || page.tasks.length > Number(params.limit)) throw new Error('Invalid workshop receipt');
    for (const entry of page.tasks) task(entry);
  } else if (method === 'workshop.result') {
    const page = object(value);
    if (page.task_id !== params.task_id || (params.run_id !== undefined && page.run_id !== params.run_id)) throw new Error('Invalid workshop receipt');
    string(page.run_id);
  }
}
