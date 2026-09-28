import { DatabaseSync, type SQLInputValue } from 'node:sqlite';
import { randomUUID } from 'node:crypto';
import { mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
import { fields, namespace, string, RpcError } from '../validation.js';

export type Row = Record<string, string | number | null>;
export function count(value: unknown, max = Number.MAX_SAFE_INTEGER): number {
  if (!Number.isSafeInteger(value) || Number(value) < 0 || Number(value) > max) throw new RpcError(-32602, 'invalid_integer');
  return Number(value);
}
function safe(value: bigint): number {
  if (value > BigInt(Number.MAX_SAFE_INTEGER) || value < -BigInt(Number.MAX_SAFE_INTEGER)) throw new RpcError(-32602, 'credit_overflow');
  return Number(value);
}
export class PlatformStore {
  readonly db: DatabaseSync;
  constructor(file: string) {
    mkdirSync(dirname(file), { recursive: true });
    this.db = new DatabaseSync(file);
    try {
      this.db.exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON;
        BEGIN IMMEDIATE;
        CREATE TABLE IF NOT EXISTS platform_migrations(version INTEGER PRIMARY KEY);
        CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,email TEXT UNIQUE NOT NULL,password TEXT NOT NULL,namespace TEXT UNIQUE NOT NULL,role TEXT NOT NULL CHECK(role IN ('user','admin')),disabled INTEGER NOT NULL DEFAULT 0,balance INTEGER NOT NULL DEFAULT 0,held INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS auth_sessions(hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES accounts(id),expires INTEGER NOT NULL);
        CREATE INDEX IF NOT EXISTS auth_expiry ON auth_sessions(expires);
        CREATE TABLE IF NOT EXISTS tariffs(version INTEGER PRIMARY KEY AUTOINCREMENT,input_micros INTEGER NOT NULL,output_micros INTEGER NOT NULL,created_at TEXT NOT NULL,actor TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS reservations(id TEXT PRIMARY KEY,namespace TEXT NOT NULL REFERENCES accounts(namespace),request_id TEXT NOT NULL,fingerprint TEXT NOT NULL,model TEXT NOT NULL,source TEXT NOT NULL,tariff_version INTEGER NOT NULL REFERENCES tariffs(version),reserved_micros INTEGER NOT NULL,status TEXT NOT NULL,settlement TEXT,charged_micros INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,UNIQUE(namespace,request_id));
        CREATE TABLE IF NOT EXISTS resolutions(key TEXT PRIMARY KEY,reservation_id TEXT UNIQUE NOT NULL REFERENCES reservations(id),payload TEXT NOT NULL,charged_micros INTEGER NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS ledger(seq INTEGER PRIMARY KEY AUTOINCREMENT,user_id TEXT NOT NULL REFERENCES accounts(id),kind TEXT NOT NULL,amount_micros INTEGER NOT NULL,reference TEXT NOT NULL,reason TEXT NOT NULL,actor TEXT NOT NULL,created_at TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS ledger_user ON ledger(user_id,seq);
        CREATE TABLE IF NOT EXISTS grants(key TEXT PRIMARY KEY,payload TEXT NOT NULL,ledger_seq INTEGER NOT NULL REFERENCES ledger(seq));
        CREATE TABLE IF NOT EXISTS audit(seq INTEGER PRIMARY KEY AUTOINCREMENT,actor TEXT NOT NULL,action TEXT NOT NULL,subject TEXT NOT NULL,detail TEXT NOT NULL,created_at TEXT NOT NULL);
        CREATE TRIGGER IF NOT EXISTS immutable_settlement BEFORE UPDATE OF settlement ON reservations WHEN OLD.settlement IS NOT NULL AND NEW.settlement IS NOT OLD.settlement BEGIN SELECT RAISE(ABORT,'immutable_receipt'); END;
        CREATE TRIGGER IF NOT EXISTS immutable_tariff_update BEFORE UPDATE ON tariffs BEGIN SELECT RAISE(ABORT,'append_only'); END;
        CREATE TRIGGER IF NOT EXISTS immutable_tariff_delete BEFORE DELETE ON tariffs BEGIN SELECT RAISE(ABORT,'append_only'); END;
        CREATE TRIGGER IF NOT EXISTS immutable_resolution_update BEFORE UPDATE ON resolutions BEGIN SELECT RAISE(ABORT,'append_only'); END;
        CREATE TRIGGER IF NOT EXISTS immutable_resolution_delete BEFORE DELETE ON resolutions BEGIN SELECT RAISE(ABORT,'append_only'); END;
        CREATE TRIGGER IF NOT EXISTS immutable_ledger_update BEFORE UPDATE ON ledger BEGIN SELECT RAISE(ABORT,'append_only'); END;
        CREATE TRIGGER IF NOT EXISTS immutable_ledger_delete BEFORE DELETE ON ledger BEGIN SELECT RAISE(ABORT,'append_only'); END;
        CREATE TRIGGER IF NOT EXISTS immutable_audit_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT,'append_only'); END;
        CREATE TRIGGER IF NOT EXISTS immutable_audit_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT,'append_only'); END;
        INSERT OR IGNORE INTO tariffs(version,input_micros,output_micros,created_at,actor) VALUES(1,1000,1000,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'bootstrap');
        INSERT OR IGNORE INTO platform_migrations VALUES(1);
        INSERT OR IGNORE INTO platform_migrations VALUES(2);
        COMMIT;`);
    } catch (e) { this.db.close(); throw e; }
  }
  get(sql: string, ...args: SQLInputValue[]): Row | undefined { return this.db.prepare(sql).get(...args) as Row | undefined; }
  all(sql: string, ...args: SQLInputValue[]): Row[] { return this.db.prepare(sql).all(...args) as Row[]; }
  run(sql: string, ...args: SQLInputValue[]) { return this.db.prepare(sql).run(...args); }
  transaction<T>(fn: () => T): T {
    this.db.exec('BEGIN IMMEDIATE');
    try { const result = fn(); this.db.exec('COMMIT'); return result; }
    catch (e) { this.db.exec('ROLLBACK'); throw e; }
  }
  audit(actor: string, action: string, subject: string, detail = '') { this.run('INSERT INTO audit(actor,action,subject,detail,created_at) VALUES(?,?,?,?,?)', actor, action, subject, detail, new Date().toISOString()); }
  account(ns: string): Row {
    const row = this.get('SELECT * FROM accounts WHERE namespace=?', ns);
    if (!row || row.disabled) throw new RpcError(-32003, 'account_unavailable');
    return row;
  }
  tariff() { return this.get('SELECT * FROM tariffs ORDER BY version DESC LIMIT 1')!; }
  setTariff(value: unknown, actor: string) {
    const p = fields(value, ['input_micros', 'output_micros']);
    const input = count(p.input_micros, 1_000_000_000), output = count(p.output_micros, 1_000_000_000);
    return this.transaction(() => {
      this.run('INSERT INTO tariffs(input_micros,output_micros,created_at,actor) VALUES(?,?,?,?)', input, output, new Date().toISOString(), actor);
      const t = this.tariff(); this.audit(actor, 'tariff_created', String(t.version), JSON.stringify({ input, output })); return t;
    });
  }
  grant(value: unknown, actor: string) {
    const p = fields(value, ['user_id', 'amount_micros', 'reason', 'idempotency_key']);
    const id = string(p.user_id), amount = count(p.amount_micros), reason = string(p.reason, 1024), key = string(p.idempotency_key, 256);
    if (amount === 0) throw new RpcError(-32602, 'invalid_amount');
    const payload = JSON.stringify({ id, amount, reason, actor });
    return this.transaction(() => {
      const previous = this.get('SELECT * FROM grants WHERE key=?', key);
      if (previous) {
        if (previous.payload !== payload) throw new RpcError(-32009, 'idempotency_conflict');
        return { ledger_seq: previous.ledger_seq, duplicate: true };
      }
      const user = this.get('SELECT * FROM accounts WHERE id=?', id);
      if (!user) throw new RpcError(-32004, 'user_not_found');
      const balance = safe(BigInt(Number(user.balance)) + BigInt(amount));
      this.run('UPDATE accounts SET balance=? WHERE id=?', balance, id);
      const seq = Number(this.run("INSERT INTO ledger(user_id,kind,amount_micros,reference,reason,actor,created_at) VALUES(?,'grant',?,?,?,?,?)", id, amount, key, reason, actor, new Date().toISOString()).lastInsertRowid);
      this.run('INSERT INTO grants VALUES(?,?,?)', key, payload, seq); this.audit(actor, 'credit_grant', id, String(seq));
      return { ledger_seq: seq, duplicate: false };
    });
  }
  walletView(ns: string, after = 0) {
    const a = this.account(ns);
    const rows = this.all('SELECT * FROM ledger WHERE user_id=? AND seq>? ORDER BY seq LIMIT 101', a.id!, after);
    return { balance_micros: a.balance, held_micros: a.held, available_micros: safe(BigInt(Number(a.balance)) - BigInt(Number(a.held))), ledger: rows.slice(0, 100), next_after: rows.length > 100 ? rows[99]!.seq : null };
  }
  usage(ns: string, offset = 0, limit = 100) {
    return this.all('SELECT id,request_id,model,source,tariff_version,reserved_micros,status,settlement,charged_micros,created_at FROM reservations WHERE namespace=? ORDER BY rowid DESC LIMIT ? OFFSET ?', ns, limit, offset).map(r => ({ ...r, settlement: r.settlement ? JSON.parse(String(r.settlement)) : null, resolution: this.resolution(String(r.id)) }));
  }
  resolution(id: string) {
    const row = this.get('SELECT payload,charged_micros,status,created_at FROM resolutions WHERE reservation_id=?', id);
    return row ? { ...row, payload: JSON.parse(String(row.payload)) } : null;
  }
  pending(offset = 0, limit = 100) { return this.all("SELECT namespace,request_id,model,source,status,reserved_micros,created_at FROM reservations WHERE status IN ('reserved','pending') ORDER BY rowid LIMIT ? OFFSET ?", limit, offset); }
  close() { this.db.close(); }
}

export class Wallet {
  constructor(private store: PlatformStore) {}
  reserve(value: unknown) {
    const p = fields(value, ['namespace', 'request_id', 'fingerprint', 'model', 'reserve_input_tokens', 'reserve_output_tokens', 'source']);
    const ns = namespace(p.namespace), id = string(p.request_id, 256), fingerprint = string(p.fingerprint, 512), model = string(p.model, 256), source = string(p.source, 128);
    const input = count(p.reserve_input_tokens), output = count(p.reserve_output_tokens);
    return this.store.transaction(() => {
      const account = this.store.account(ns);
      const previous = this.store.get('SELECT * FROM reservations WHERE namespace=? AND request_id=?', ns, id);
      if (previous) {
        if (previous.fingerprint !== fingerprint) throw new RpcError(-32009, 'idempotency_conflict');
        return this.receipt(previous, true);
      }
      const tariff = this.store.tariff();
      const amount = this.price(input, output, tariff);
      const held = safe(BigInt(Number(account.held)) + BigInt(amount));
      if (Number(account.balance) < held || Number(account.balance) < 0) throw new RpcError(-32002, 'insufficient_credits');
      const reservation = randomUUID();
      this.store.run("INSERT INTO reservations(id,namespace,request_id,fingerprint,model,source,tariff_version,reserved_micros,status,created_at) VALUES(?,?,?,?,?,?,?,?,'reserved',?)", reservation, ns, id, fingerprint, model, source, tariff.version!, amount, new Date().toISOString());
      this.store.run('UPDATE accounts SET held=? WHERE id=?', held, account.id!);
      this.store.audit(source, 'reserve', reservation, String(amount));
      return { reservation_id: reservation, tariff_version: tariff.version, reserved_micros: amount, duplicate: false };
    });
  }
  private receipt(row: Row, duplicate: boolean) { return { reservation_id: row.id, tariff_version: row.tariff_version, reserved_micros: row.reserved_micros, duplicate }; }
  private price(input: number, output: number, tariff: Row) { return safe(BigInt(input) * BigInt(Number(tariff.input_micros)) + BigInt(output) * BigInt(Number(tariff.output_micros))); }
  resolve(value: unknown, actor: string) {
    const p = fields(value, ['namespace', 'request_id', 'decision', 'usage', 'reason', 'idempotency_key']);
    const ns = namespace(p.namespace), request = string(p.request_id, 256), reason = string(p.reason, 1024), key = string(p.idempotency_key, 256);
    if (typeof p.decision !== 'string' || !['release', 'settle'].includes(p.decision)) throw new RpcError(-32602, 'invalid_decision');
    let usage: Record<string, unknown> | undefined;
    if (p.decision === 'settle') {
      const u = fields(p.usage, ['known', 'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens']);
      if (u.known !== true) throw new RpcError(-32602, 'resolution_requires_known_usage');
      const input = count(u.input_tokens), output = count(u.output_tokens);
      const read = u.cache_read_tokens === undefined ? undefined : count(u.cache_read_tokens), write = u.cache_write_tokens === undefined ? undefined : count(u.cache_write_tokens);
      if (BigInt(read ?? 0) + BigInt(write ?? 0) > BigInt(input)) throw new RpcError(-32602, 'invalid_cache_subset');
      usage = { known: true, input_tokens: input, output_tokens: output, ...(read === undefined ? {} : { cache_read_tokens: read }), ...(write === undefined ? {} : { cache_write_tokens: write }) };
    } else if (p.usage !== undefined) throw new RpcError(-32602, 'release_has_usage');
    const payload = JSON.stringify({ namespace: ns, request_id: request, decision: p.decision, ...(usage ? { usage } : {}), reason, actor });
    return this.store.transaction(() => {
      const previous = this.store.get('SELECT * FROM resolutions WHERE key=?', key);
      if (previous) {
        if (previous.payload !== payload) throw new RpcError(-32009, 'idempotency_conflict');
        return { reservation_id: previous.reservation_id, status: previous.status, charged_micros: previous.charged_micros, duplicate: true };
      }
      const row = this.store.get('SELECT * FROM reservations WHERE namespace=? AND request_id=?', ns, request);
      if (!row) throw new RpcError(-32004, 'reservation_not_found');
      if (!['reserved', 'pending'].includes(String(row.status)) || this.store.resolution(String(row.id))) throw new RpcError(-32009, 'reservation_not_pending');
      const a = this.store.get('SELECT * FROM accounts WHERE namespace=?', ns)!;
      const tariff = this.store.get('SELECT * FROM tariffs WHERE version=?', row.tariff_version!)!;
      const charge = usage ? this.price(Number(usage.input_tokens), Number(usage.output_tokens), tariff) : 0;
      const balance = safe(BigInt(Number(a.balance)) - BigInt(charge)), held = safe(BigInt(Number(a.held)) - BigInt(Number(row.reserved_micros)));
      safe(BigInt(balance) - BigInt(held));
      const status = usage ? 'resolved_settled' : 'resolved_released', now = new Date().toISOString();
      this.store.run('INSERT INTO resolutions VALUES(?,?,?,?,?,?)', key, row.id!, payload, charge, status, now);
      this.store.run('UPDATE accounts SET balance=?,held=? WHERE id=?', balance, held, a.id!);
      this.store.run('UPDATE reservations SET status=?,charged_micros=? WHERE id=?', status, charge, row.id!);
      this.store.run('INSERT INTO ledger(user_id,kind,amount_micros,reference,reason,actor,created_at) VALUES(?,?,?,?,?,?,?)', a.id!, status, -charge, row.id!, reason, actor, now);
      this.store.audit(actor, status, String(row.id), payload);
      return { reservation_id: row.id, status, charged_micros: charge, duplicate: false };
    });
  }
  settle(value: unknown) {
    const p = fields(value, ['namespace', 'request_id', 'usage', 'outcome', 'provider_status', 'source']);
    const ns = namespace(p.namespace), id = string(p.request_id, 256), source = string(p.source, 128);
    if (typeof p.outcome !== 'string' || !['complete', 'rejected', 'uncertain'].includes(p.outcome)) throw new RpcError(-32602, 'invalid_outcome');
    const u = fields(p.usage, ['known', 'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens']);
    if (typeof u.known !== 'boolean') throw new RpcError(-32602, 'invalid_usage');
    const input = count(u.input_tokens), output = count(u.output_tokens);
    const read = u.cache_read_tokens === undefined ? undefined : count(u.cache_read_tokens), write = u.cache_write_tokens === undefined ? undefined : count(u.cache_write_tokens);
    if (BigInt(read ?? 0) + BigInt(write ?? 0) > BigInt(input)) throw new RpcError(-32602, 'invalid_cache_subset');
    if (!u.known && (input !== 0 || output !== 0 || (read ?? 0) !== 0 || (write ?? 0) !== 0)) throw new RpcError(-32602, 'unknown_usage_has_counts');
    if (p.outcome === 'rejected' && (input !== 0 || output !== 0)) throw new RpcError(-32602, 'rejected_usage_conflict');
    const provider = p.provider_status === undefined ? undefined : count(p.provider_status, 599);
    const payload = JSON.stringify({ usage: { known: u.known, input_tokens: input, output_tokens: output, ...(read === undefined ? {} : { cache_read_tokens: read }), ...(write === undefined ? {} : { cache_write_tokens: write }) }, outcome: p.outcome, ...(provider === undefined ? {} : { provider_status: provider }), source });
    return this.store.transaction(() => {
      // Disabled accounts still accept receipts: revocation cannot erase incurred usage.
      const account = this.store.get('SELECT * FROM accounts WHERE namespace=?', ns);
      const row = this.store.get('SELECT * FROM reservations WHERE namespace=? AND request_id=?', ns, id);
      if (!account || !row) throw new RpcError(-32004, 'reservation_not_found');
      if (row.settlement !== null) {
        if (row.settlement !== payload) throw new RpcError(-32009, 'settlement_conflict');
        return { reservation_id: row.id, status: row.status, charged_micros: row.charged_micros, duplicate: true };
      }
      if (this.store.resolution(String(row.id))) {
        // Manual reconciliation already adjusted credits. Preserve a late provider
        // receipt as evidence without changing its charge or releasing another hold.
        this.store.run('UPDATE reservations SET settlement=? WHERE id=?', payload, row.id!);
        this.store.audit(source, 'late_receipt', String(row.id), payload);
        return { reservation_id: row.id, status: row.status, charged_micros: row.charged_micros, duplicate: false, late: true };
      }
      const pending = p.outcome !== 'rejected' && !u.known;
      const status = pending ? 'pending' : p.outcome === 'rejected' ? 'released' : 'settled';
      const tariff = this.store.get('SELECT * FROM tariffs WHERE version=?', row.tariff_version!)!;
      const charge = pending || p.outcome === 'rejected' ? 0 : this.price(input, output, tariff);
      if (!pending) {
        const balance = safe(BigInt(Number(account.balance)) - BigInt(charge));
        const held = safe(BigInt(Number(account.held)) - BigInt(Number(row.reserved_micros)));
        // Guard future available balance arithmetic as well as each stored integer.
        safe(BigInt(balance) - BigInt(held));
        this.store.run('UPDATE accounts SET balance=?,held=? WHERE id=?', balance, held, account.id!);
        this.store.run('INSERT INTO ledger(user_id,kind,amount_micros,reference,reason,actor,created_at) VALUES(?,?,?,?,?,?,?)', account.id!, status, -charge, row.id!, String(p.outcome), source, new Date().toISOString());
      }
      this.store.run('UPDATE reservations SET status=?,settlement=?,charged_micros=? WHERE id=?', status, payload, charge, row.id!);
      this.store.audit(source, status, String(row.id), payload);
      return { reservation_id: row.id, status, charged_micros: charge, duplicate: false };
    });
  }
}
