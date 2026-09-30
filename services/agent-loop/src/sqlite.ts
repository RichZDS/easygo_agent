import { DatabaseSync, type SQLInputValue, type StatementSync } from 'node:sqlite';
import { mkdirSync } from 'node:fs';
import { dirname } from 'node:path';

export type Row = Record<string, string | number | null>;
export const PRAGMAS =
  'PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON;';

// One connection plus a statement cache keyed by SQL text. node:sqlite resets a statement
// after every run/get/all, so a cached statement is safe to reuse.
export class Database {
  private statements = new Map<string, StatementSync>();
  constructor(private db: DatabaseSync) {}
  prepare(sql: string): StatementSync {
    let statement = this.statements.get(sql);
    if (!statement) this.statements.set(sql, (statement = this.db.prepare(sql)));
    return statement;
  }
  get<T = Row>(sql: string, ...args: SQLInputValue[]): T | undefined {
    return this.prepare(sql).get(...args) as T | undefined;
  }
  all<T = Row>(sql: string, ...args: SQLInputValue[]): T[] {
    return this.prepare(sql).all(...args) as T[];
  }
  run(sql: string, ...args: SQLInputValue[]) {
    return this.prepare(sql).run(...args);
  }
  exec(sql: string): void {
    this.db.exec(sql);
  }
  // `before` runs inside the transaction, ahead of `fn` (the core store checks its lease there).
  transaction<T>(fn: () => T, { before }: { before?: () => void } = {}): T {
    this.db.exec('BEGIN IMMEDIATE');
    try {
      before?.();
      const result = fn();
      this.db.exec('COMMIT');
      return result;
    } catch (error) {
      this.db.exec('ROLLBACK');
      throw error;
    }
  }
  close(): void {
    this.statements.clear();
    this.db.close();
  }
}

// Creates the parent directory, applies the pragmas, then the store's own DDL. The
// connection is closed again if either step fails.
export function openDatabase(file: string, ddl: string, pragmas = PRAGMAS): Database {
  mkdirSync(dirname(file), { recursive: true });
  const db = new DatabaseSync(file);
  try {
    db.exec(pragmas);
    db.exec(ddl);
  } catch (error) {
    db.close();
    throw error;
  }
  return new Database(db);
}
