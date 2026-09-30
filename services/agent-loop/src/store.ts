import { randomUUID } from 'node:crypto';
import { openDatabase, type Database, type Row } from './sqlite.js';
import type { Message, Response, Run } from './types.js';
import { RpcError } from './validation.js';

const LEASE_MS = 30_000;
const DDL = `
        CREATE TABLE IF NOT EXISTS ownership (id INTEGER PRIMARY KEY CHECK(id=1), owner TEXT NOT NULL, expires INTEGER NOT NULL);
        CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, namespace TEXT NOT NULL, created_at TEXT NOT NULL, context TEXT NOT NULL DEFAULT '[]');
        CREATE TABLE IF NOT EXISTS runs (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL, namespace TEXT NOT NULL, session_id TEXT NOT NULL REFERENCES sessions(id), status TEXT NOT NULL, created_at TEXT NOT NULL, input TEXT NOT NULL, idempotency_key TEXT NOT NULL, result TEXT, error TEXT, UNIQUE(namespace, session_id, idempotency_key));
        CREATE INDEX IF NOT EXISTS runs_queue ON runs(status, seq);
        CREATE TABLE IF NOT EXISTS messages (seq INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL REFERENCES sessions(id), run_id TEXT NOT NULL REFERENCES runs(id), message TEXT NOT NULL, metadata TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS events (seq INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES runs(id), kind TEXT NOT NULL, data TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS messages_session ON messages(session_id, seq);
        CREATE INDEX IF NOT EXISTS events_run ON events(run_id, seq);
        CREATE TABLE IF NOT EXISTS knowledge_outbox (run_id TEXT PRIMARY KEY REFERENCES runs(id), namespace TEXT NOT NULL);`;
export class Store {
  private db: Database;
  private owner = randomUUID();
  private closed = false;
  constructor(file: string) {
    this.db = openDatabase(file, DDL);
    try {
      this.db.transaction(() => {
        const previous = this.db.get('SELECT * FROM ownership WHERE id=1');
        if (previous && Number(previous.expires) > Date.now())
          throw new RpcError(-32009, 'database_owned', 'Database already has an active owner');
        this.db.run(
          'INSERT INTO ownership VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner, expires=excluded.expires',
          this.owner,
          Date.now() + LEASE_MS
        );
        if (!this.db.all('PRAGMA table_info(runs)').some((r) => r.name === 'workshop_runtime'))
          this.db.exec("ALTER TABLE runs ADD COLUMN workshop_runtime TEXT NOT NULL DEFAULT ''");
        for (const row of this.db.all("SELECT id FROM runs WHERE status='running'")) {
          const error = { code: 'interrupted', message: 'Previous owner stopped; tools will not be replayed' };
          this.db.run("UPDATE runs SET status='interrupted', error=? WHERE id=?", JSON.stringify(error), row.id!);
          this.event(String(row.id), 'terminal', { status: 'interrupted', error });
        }
      });
    } catch (error) {
      this.db.close();
      throw error;
    }
  }
  assertOwner() {
    if (this.closed) throw new RpcError(-32603, 'storage_closed');
    const row = this.db.get('SELECT * FROM ownership WHERE id=1');
    if (row?.owner !== this.owner || Number(row?.expires) <= Date.now()) throw new RpcError(-32603, 'ownership_lost');
  }
  transaction<T>(fn: () => T): T {
    return this.db.transaction(fn, { before: () => this.assertOwner() });
  }
  heartbeat() {
    this.transaction(() =>
      this.db.run('UPDATE ownership SET expires=? WHERE id=1 AND owner=?', Date.now() + LEASE_MS, this.owner)
    );
  }
  close() {
    if (this.closed) return;
    try {
      this.db.run('DELETE FROM ownership WHERE id=1 AND owner=?', this.owner);
    } finally {
      this.closed = true;
      this.db.close();
    }
  }
  createSession(ns: string) {
    return this.transaction(() => {
      const session = { id: randomUUID(), namespace: ns, created_at: new Date().toISOString() };
      this.db.run('INSERT INTO sessions(id,namespace,created_at) VALUES(?,?,?)', session.id, ns, session.created_at);
      return session;
    });
  }
  session(ns: string, id: string): Row {
    this.assertOwner();
    const row = this.db.get('SELECT * FROM sessions WHERE namespace=? AND id=?', ns, id);
    if (!row) throw new RpcError(-32004, 'session_not_found');
    return row;
  }
  listSessions(ns: string, offset: number, limit: number) {
    this.assertOwner();
    const sessions = this.db.all(
      'SELECT id,namespace,created_at FROM sessions WHERE namespace=? ORDER BY rowid LIMIT ? OFFSET ?',
      ns,
      limit + 1,
      offset
    );
    return { sessions: sessions.slice(0, limit), next_offset: sessions.length > limit ? offset + limit : null };
  }
  history(ns: string, id: string, after: number, limit: number, before?: number) {
    this.session(ns, id);
    if (before !== undefined && after !== 0) throw new RpcError(-32602, 'conflicting_cursors');
    const rows =
      before === undefined
        ? this.db.all(
            'SELECT seq,run_id,message,metadata FROM messages WHERE session_id=? AND seq>? ORDER BY seq LIMIT ?',
            id,
            after,
            limit + 1
          )
        : this.db.all(
            'SELECT seq,run_id,message,metadata FROM messages WHERE session_id=? AND seq<? ORDER BY seq DESC LIMIT ?',
            id,
            before,
            limit + 1
          );
    const page = rows.slice(0, limit);
    if (before !== undefined) page.reverse();
    const runRows = this.db.all(
      "SELECT * FROM runs WHERE namespace=? AND session_id=? ORDER BY CASE WHEN status IN ('queued','running') THEN 0 ELSE 1 END, seq DESC LIMIT 101",
      ns,
      id
    );
    return {
      runs: runRows.slice(0, 100).map((row) => {
        const raw = String(row.input);
        const points = [...raw];
        return { ...this.runView(row), input: points.slice(0, 1024).join(''), input_truncated: points.length > 1024 };
      }),
      runs_truncated: runRows.length > 100,
      messages: page.map((r) => ({
        seq: r.seq,
        run_id: r.run_id,
        ...JSON.parse(String(r.message)),
        metadata: JSON.parse(String(r.metadata)),
      })),
      next_after: before === undefined && rows.length > limit ? (rows[limit - 1]?.seq ?? after) : null,
      previous_before: before !== undefined && rows.length > limit ? (page[0]?.seq ?? null) : null,
    };
  }
  start(ns: string, session: string, input: string, key: string, runtime = ''): Run {
    return this.transaction(() => {
      this.session(ns, session);
      const existing = this.db.get(
        'SELECT * FROM runs WHERE namespace=? AND session_id=? AND idempotency_key=?',
        ns,
        session,
        key
      );
      if (existing) {
        if (existing.input !== input || (runtime !== '' && (existing.workshop_runtime ?? '') !== runtime))
          throw new RpcError(-32009, 'idempotency_conflict');
        return this.runView(existing);
      }
      const count = this.db.get("SELECT count(*) AS n FROM runs WHERE status IN ('running','queued')")!;
      if (Number(count.n) >= 1000) throw new RpcError(-32029, 'queue_full');
      const id = randomUUID();
      this.db.run(
        "INSERT INTO runs(id,namespace,session_id,status,created_at,input,idempotency_key,workshop_runtime) VALUES(?,?,?,'queued',?,?,?,?)",
        id,
        ns,
        session,
        new Date().toISOString(),
        input,
        key,
        runtime
      );
      this.event(id, 'queued', {});
      return this.get(ns, id);
    });
  }
  private runView(row: Row): Run {
    return {
      ...(row.workshop_runtime ? { workshop_runtime: String(row.workshop_runtime) } : {}),
      id: String(row.id),
      namespace: String(row.namespace),
      session_id: String(row.session_id),
      status: row.status as Run['status'],
      created_at: String(row.created_at),
      ...(row.result ? { result: JSON.parse(String(row.result)) } : {}),
      ...(row.error ? { error: JSON.parse(String(row.error)) } : {}),
    };
  }
  get(ns: string, id: string): Run {
    this.assertOwner();
    const row = this.db.get('SELECT * FROM runs WHERE namespace=? AND id=?', ns, id);
    if (!row) throw new RpcError(-32004, 'run_not_found');
    return this.runView(row);
  }
  next(busy: Set<string>): Run | undefined {
    this.assertOwner();
    const rows = this.db.all("SELECT * FROM runs WHERE status='queued' ORDER BY seq LIMIT 1000");
    const row = rows.find((r) => !busy.has(String(r.session_id)));
    return row && this.runView(row);
  }
  begin(run: Run): Message[] {
    return this.transaction(() => {
      const row = this.db.get("SELECT input FROM runs WHERE id=? AND status='queued'", run.id);
      if (!row) throw new RpcError(-32009, 'run_not_queued');
      const context = JSON.parse(String(this.session(run.namespace, run.session_id).context)) as Message[];
      const user: Message = { role: 'user', content: [{ type: 'text', text: String(row.input) }] };
      this.db.run("UPDATE runs SET status='running' WHERE id=?", run.id);
      this.message(run, user, {});
      this.event(run.id, 'running', {});
      return [...context, user];
    });
  }
  private message(run: Run, message: Message, metadata: unknown) {
    this.db.run(
      'INSERT INTO messages(session_id,run_id,message,metadata) VALUES(?,?,?,?)',
      run.session_id,
      run.id,
      JSON.stringify(message),
      JSON.stringify(metadata)
    );
  }
  private event(run: string, kind: string, data: unknown) {
    this.db.run('INSERT INTO events(run_id,kind,data) VALUES(?,?,?)', run, kind, JSON.stringify(data));
  }
  recordEvent(run: Run, kind: string, data: unknown) {
    this.transaction(() => {
      this.requireRunning(run);
      this.event(run.id, kind, data);
    });
  }
  private requireRunning(run: Run) {
    if (this.get(run.namespace, run.id).status !== 'running') throw new RpcError(-32009, 'run_not_running');
  }
  append(run: Run, message: Message, metadata: unknown) {
    this.transaction(() => {
      this.requireRunning(run);
      this.message(run, message, metadata);
      this.event(run.id, 'committed', { role: message.role, metadata });
    });
  }
  finish(run: Run, status: Run['status'], context?: Message[], response?: Response, error?: Run['error']) {
    return this.transaction(() => {
      const current = this.get(run.namespace, run.id);
      if (current.status !== 'running' && current.status !== 'queued') return current;
      if (status === 'completed') {
        if (!context || !response || current.status !== 'running') throw new Error('Invalid completion');
        this.message(run, response.message, {
          usage: response.usage,
          cost: response.cost,
          finish_reason: response.finish_reason,
        });
        this.db.run('UPDATE sessions SET context=? WHERE id=?', JSON.stringify(context), run.session_id);
        this.db.run('INSERT OR IGNORE INTO knowledge_outbox(run_id,namespace) VALUES(?,?)', run.id, run.namespace);
      }
      this.db.run(
        'UPDATE runs SET status=?,result=?,error=? WHERE id=?',
        status,
        response ? JSON.stringify(response) : null,
        error ? JSON.stringify(error) : null,
        run.id
      );
      this.event(run.id, 'terminal', { status, ...(error ? { error } : {}) });
      return this.get(run.namespace, run.id);
    });
  }
  knowledgePending(limit = 100): Array<{ namespace: string; run_id: string; messages: Message[] }> {
    this.assertOwner();
    const rows = this.db.all('SELECT namespace,run_id FROM knowledge_outbox ORDER BY rowid LIMIT ?', limit);
    return rows.map((row) => ({
      namespace: String(row.namespace),
      run_id: String(row.run_id),
      messages: this.db
        .all('SELECT message FROM messages WHERE run_id=? ORDER BY seq', row.run_id!)
        .map((m) => JSON.parse(String(m.message)) as Message),
    }));
  }
  acknowledgeKnowledge(runID: string) {
    this.transaction(() => this.db.run('DELETE FROM knowledge_outbox WHERE run_id=?', runID));
  }
  events(ns: string, id: string, after: number, limit: number) {
    this.get(ns, id);
    const rows = this.db.all(
      'SELECT * FROM events WHERE run_id=? AND seq>? ORDER BY seq LIMIT ?',
      id,
      after,
      limit + 1
    );
    return {
      events: rows
        .slice(0, limit)
        .map((r) => ({ seq: r.seq, run_id: r.run_id, kind: r.kind, data: JSON.parse(String(r.data)) })),
      next_after: rows.length > limit ? (rows[limit - 1]?.seq ?? after) : null,
    };
  }
}
