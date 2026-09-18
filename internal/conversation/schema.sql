CREATE TABLE IF NOT EXISTS agent_sessions (
 id uuid PRIMARY KEY,
 username text NOT NULL,
 context_messages jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_sessions_user_updated ON agent_sessions(username, updated_at DESC, id);
CREATE TABLE IF NOT EXISTS agent_turns (
 id bigserial PRIMARY KEY,
 session_id uuid NOT NULL REFERENCES agent_sessions(id),
 status text NOT NULL CHECK (status IN ('completed', 'failed', 'canceled')),
 messages jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_turns_session_id ON agent_turns(session_id, id);

CREATE TABLE IF NOT EXISTS agent_runs (
 id uuid PRIMARY KEY,
 session_id uuid NOT NULL REFERENCES agent_sessions(id),
 input text NOT NULL,
 status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'completed', 'failed', 'canceled')),
 idempotency_key text,
 cancel_requested boolean NOT NULL DEFAULT false,
 worker_id text,
 claim_token uuid,
 lease_expires_at timestamptz,
 result_text text,
 error text,
 turn_id bigint REFERENCES agent_turns(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 started_at timestamptz,
 finished_at timestamptz
);
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS claim_token uuid;
CREATE UNIQUE INDEX IF NOT EXISTS agent_runs_session_idempotency
 ON agent_runs(session_id, idempotency_key)
 WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';
CREATE INDEX IF NOT EXISTS agent_runs_queue_order
 ON agent_runs(session_id, status, created_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS agent_runs_one_running_per_session
 ON agent_runs(session_id)
 WHERE status = 'running';

ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS notification_id text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS agent_runs_notification ON agent_runs(notification_id) WHERE notification_id <> '';
-- Kept here so existing run commits work even when task workers are disabled.
CREATE TABLE IF NOT EXISTS agent_task_notifications (
 id text PRIMARY KEY, username text NOT NULL,
 session_id uuid NOT NULL REFERENCES agent_sessions(id), content text NOT NULL,
 enqueued boolean NOT NULL DEFAULT false, acknowledged boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
