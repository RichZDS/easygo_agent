import type { RpcClient } from '../rpc.js';
import { fields, object, string } from '../validation.js';
import { check, enumValue, numberValue, statuses } from './receipts.js';
import { callWorkshop } from './workshop.js';

const kinds = ['state', 'session', 'text', 'result', 'diagnostic', 'crew.message', 'crew.read', 'acceptance'];
const visible = new Set(['state', 'crew.message', 'crew.read', 'acceptance']);
const budget = 32768;
const size = (value: unknown) => Buffer.byteLength(JSON.stringify(value));

function validateEvents(value: unknown, params: Record<string, unknown>, runIDs: Set<string>): void {
  check(Array.isArray(value) && value.length <= 1000);
  let previous = Number(params.after);
  for (const item of value) {
    const event = fields(item, [
      'sequence',
      'run_id',
      'time',
      'kind',
      'text',
      'session_id',
      'usage',
      'message',
      'read',
      'acceptance',
      'namespace',
      'task_id',
    ]);
    previous = numberValue(event.sequence, previous + 1);
    check(runIDs.has(string(event.run_id)));
    check(Number.isFinite(Date.parse(string(event.time))));
    enumValue(event.kind, kinds);
    if (event.namespace !== undefined) check(event.namespace === params.namespace);
    if (event.task_id !== undefined) check(event.task_id === params.task_id);
    if (event.text !== undefined) check(typeof event.text === 'string');
    if (event.session_id !== undefined) string(event.session_id, 4096);
    if (event.usage !== undefined) {
      const usage = fields(event.usage, ['input_tokens', 'output_tokens', 'cached_input_tokens']);
      numberValue(usage.input_tokens);
      numberValue(usage.output_tokens);
      if (usage.cached_input_tokens !== undefined) numberValue(usage.cached_input_tokens);
    }
    for (const [field, kind] of [
      ['message', 'crew.message'],
      ['read', 'crew.read'],
      ['acceptance', 'acceptance'],
    ]) {
      check((event[field!] !== undefined) === (event.kind === kind));
    }
    if (event.kind === 'state') enumValue(event.text, statuses);
    if (event.kind === 'crew.message') {
      const message = fields(event.message, ['id', 'direction', 'kind', 'text', 'claims', 'client_id']);
      string(message.id);
      string(message.text, 8192);
      enumValue(message.direction, ['from_worker', 'to_worker']);
      enumValue(message.kind, message.direction === 'to_worker' ? ['note'] : ['report', 'ask', 'blocked', 'submit']);
      if (message.client_id !== undefined) check(/^[A-Za-z0-9_-]{1,64}$/.test(string(message.client_id, 64)));
      check((message.claims !== undefined) === (message.kind === 'submit'));
      if (message.claims !== undefined) enumValue(fields(message.claims, ['tests']).tests, ['pass', 'fail', 'not_run']);
    }
    if (event.kind === 'crew.read') {
      const read = fields(event.read, ['message_ids']);
      check(Array.isArray(read.message_ids));
      const ids = read.message_ids.map((id) => string(id));
      check(new Set(ids).size === ids.length);
    }
    if (event.kind === 'acceptance') {
      const acceptance = fields(event.acceptance, ['state', 'check', 'exit_code', 'evidence_id', 'false_green']);
      enumValue(acceptance.state, ['running', 'passed', 'failed', 'error', 'cancelled', 'interrupted']);
      if (acceptance.check !== undefined) check(/^[a-z0-9-]{1,32}$/.test(string(acceptance.check)));
      if (acceptance.exit_code !== undefined) numberValue(acceptance.exit_code, -2147483648, 2147483647);
      if (acceptance.evidence_id !== undefined) string(acceptance.evidence_id);
      if (acceptance.false_green !== undefined) check(typeof acceptance.false_green === 'boolean');
    }
  }
}

export async function readMessages(
  client: RpcClient,
  params: Record<string, unknown>,
  signal: AbortSignal
): Promise<unknown> {
  let runIDs = new Set<string>();
  // Summaries only carry the latest run; run_ids authenticates historical runs too.
  await callWorkshop(
    client,
    'workshop.get',
    { namespace: params.namespace, task_id: params.task_id },
    signal,
    false,
    (value) => {
      const task = object(value);
      check(Array.isArray(task.run_ids) && task.run_ids.length <= 256);
      const ids = task.run_ids.map((id) => string(id));
      check(new Set(ids).size === ids.length);
      runIDs = new Set(ids);
      if (task.runs !== undefined) for (const run of task.runs as unknown[]) check(runIDs.has(string(object(run).id)));
    }
  );
  const events = (await callWorkshop(client, 'workshop.events', params, signal, false, (value) =>
    validateEvents(value, params, runIDs)
  )) as Record<string, unknown>[];
  const page = {
    namespace: params.namespace,
    task_id: params.task_id,
    events: [] as Record<string, unknown>[],
    next: Number(params.after),
    truncated: events.length === 1000,
  };
  for (const event of events) {
    if (!visible.has(String(event.kind))) {
      page.next = Number(event.sequence);
      continue;
    }
    // Project only the validated payload appropriate to this kind. Native result
    // text and optional metadata cannot accidentally flood the model context.
    const entry: Record<string, unknown> = {
      sequence: event.sequence,
      run_id: event.run_id,
      time: event.time,
      kind: event.kind,
    };
    const field =
      event.kind === 'crew.message'
        ? 'message'
        : event.kind === 'crew.read'
          ? 'read'
          : event.kind === 'acceptance'
            ? 'acceptance'
            : 'text';
    entry[field] = event[field];
    if (size({ ...page, events: [...page.events, entry], next: event.sequence }) > budget) {
      if (page.events.length) {
        page.truncated = true;
        break;
      }
      // A legal 8 KiB message can exceed the JSON budget when escaped. Preserve
      // its identity, explicitly mark a preview, and avoid a nonadvancing cursor.
      if (entry.kind === 'crew.message') {
        const message = { ...object(entry.message) };
        const chars = Array.from(String(message.text));
        entry.message = message;
        entry.text_truncated = true;
        let low = 0,
          high = chars.length;
        while (low < high) {
          const mid = Math.ceil((low + high) / 2);
          message.text = chars.slice(0, mid).join('');
          if (size({ ...page, events: [entry], next: event.sequence }) <= budget) low = mid;
          else high = mid - 1;
        }
        message.text = chars.slice(0, low).join('');
      } else {
        const read = { ...object(entry.read) };
        const ids = read.message_ids as string[];
        entry.read = read;
        entry.message_ids_truncated = true;
        let low = 0,
          high = ids.length;
        while (low < high) {
          const mid = Math.ceil((low + high) / 2);
          read.message_ids = ids.slice(0, mid);
          if (size({ ...page, events: [entry], next: event.sequence }) <= budget) low = mid;
          else high = mid - 1;
        }
        read.message_ids = ids.slice(0, low);
      }
    }
    page.events.push(entry);
    page.next = Number(event.sequence);
  }
  return page;
}
