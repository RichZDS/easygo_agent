export class RpcError extends Error {
  constructor(public readonly code: number, public readonly reason: string, message = 'Request failed', public readonly details?: unknown) { super(message); }
  wire() { return { code: this.code, message: this.message, data: { code: this.reason, ...(this.details === undefined ? {} : { upstream: this.details }) } }; }
}
export function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new RpcError(-32602, 'invalid_params');
  return value as Record<string, unknown>;
}
export function fields(value: unknown, allowed: string[]): Record<string, unknown> {
  const o = object(value);
  if (Object.keys(o).some(k => !allowed.includes(k))) throw new RpcError(-32602, 'unknown_field');
  return o;
}
export function string(value: unknown, max = 128): string {
  if (typeof value !== 'string' || !value.length || Buffer.byteLength(value) > max) throw new RpcError(-32602, 'invalid_string');
  return value;
}
export function integer(value: unknown, fallback: number, max: number, min = 0): number {
  if (value === undefined) return fallback;
  if (!Number.isSafeInteger(value) || (value as number) < min || (value as number) > max) throw new RpcError(-32602, 'invalid_integer');
  return value as number;
}
export function namespace(value: unknown): string {
  const s = string(value);
  if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/.test(s)) throw new RpcError(-32602, 'invalid_namespace');
  return s;
}
export function rpcFailure(error: unknown): RpcError {
  return error instanceof RpcError ? error : new RpcError(-32603, 'internal_error');
}
