import { object, string } from '../validation.js';

export const statuses = [
  'queued',
  'running',
  'cancelling',
  'succeeded',
  'failed',
  'cancelled',
  'timed_out',
  'interrupted',
];
const acceptanceStates = ['skipped', 'running', 'passed', 'failed', 'error', 'cancelled', 'interrupted'];
export function check(valid: boolean): asserts valid {
  if (!valid) throw new Error('Invalid workshop receipt');
}
export function enumValue(value: unknown, values: string[]): void {
  check(typeof value === 'string' && values.includes(value));
}
export function numberValue(value: unknown, min = 0, max = Number.MAX_SAFE_INTEGER): number {
  check(Number.isSafeInteger(value) && Number(value) >= min && Number(value) <= max);
  return Number(value);
}
function booleanValue(value: unknown): void {
  check(typeof value === 'boolean');
}
function timeValue(value: unknown): void {
  check(Number.isFinite(Date.parse(string(value))));
}
export function validateRunSummary(value: unknown): void {
  const run = object(value);
  string(run.id);
  enumValue(run.status, statuses);
  if (run.outcome !== undefined) enumValue(run.outcome, ['submitted', 'blocked', 'asked', 'none']);
  if (run.acceptance_state !== undefined) enumValue(run.acceptance_state, acceptanceStates);
  if (run.false_green !== undefined) booleanValue(run.false_green);
  if (run.evidence_count !== undefined) numberValue(run.evidence_count, 0, 8);
}

export function validateHarnessReceipt(method: string, params: Record<string, unknown>, value: unknown): void {
  if (!['workshop.message', 'workshop.evidence'].includes(method)) return;
  const receipt = object(value);
  check(receipt.namespace === params.namespace && receipt.task_id === params.task_id);
  if (method === 'workshop.message') {
    string(receipt.id);
    numberValue(receipt.sequence, 1);
    check(Buffer.byteLength(JSON.stringify(receipt)) <= 1024);
    return;
  }
  string(receipt.run_id);
  if (params.run_id !== undefined) check(receipt.run_id === params.run_id);
  // Bound metadata as well as text; malformed remote data never reaches the model.
  check(Buffer.byteLength(JSON.stringify(receipt)) <= 256 * 1024);
  if (params.evidence_id !== undefined) {
    check(receipt.evidence_id === params.evidence_id);
    check(typeof receipt.text === 'string' && Buffer.byteLength(receipt.text) <= Number(params.limit));
    const offset = numberValue(receipt.offset),
      next = numberValue(receipt.next_offset),
      total = numberValue(receipt.total_bytes);
    check(offset === params.offset && next === offset + Buffer.byteLength(receipt.text) && next <= total);
    booleanValue(receipt.eof);
    check(receipt.eof === (next === total) && (receipt.eof || next > offset));
  } else {
    enumValue(receipt.acceptance_state, acceptanceStates);
    booleanValue(receipt.false_green);
    check(Array.isArray(receipt.evidence) && receipt.evidence.length <= 8);
    const ids = new Set<string>();
    for (const item of receipt.evidence) {
      const evidence = object(item),
        id = string(evidence.id);
      check(!ids.has(id));
      ids.add(id);
      check(/^[a-z0-9-]{1,32}$/.test(string(evidence.check)));
      check(Array.isArray(evidence.command) && evidence.command.length > 0);
      for (const arg of evidence.command) check(typeof arg === 'string');
      check(String(evidence.command[0]).startsWith('/'));
      numberValue(evidence.exit_code, -2147483648, 2147483647);
      booleanValue(evidence.timed_out);
      numberValue(evidence.duration_ms);
      numberValue(evidence.output_bytes);
      booleanValue(evidence.output_truncated);
      check(/^[a-f0-9]{64}$/.test(string(evidence.workspace_sha256)));
      timeValue(evidence.time);
    }
  }
}
