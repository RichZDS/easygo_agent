CREATE TABLE IF NOT EXISTS agent_tasks (
 id uuid PRIMARY KEY,
 username text NOT NULL,
 session_id uuid NOT NULL REFERENCES agent_sessions(id),
 parent_run_id uuid NOT NULL REFERENCES agent_runs(id),
 request_key text NOT NULL,
 status text NOT NULL CHECK(status IN ('queued','running','blocked','completed','failed','canceled')),
 version integer NOT NULL,
 state jsonb NOT NULL,
 claim_token uuid,
 lease_expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(parent_run_id,request_key)
);
CREATE INDEX IF NOT EXISTS agent_tasks_claim ON agent_tasks(status,created_at);
CREATE INDEX IF NOT EXISTS agent_tasks_owner ON agent_tasks(username,session_id,created_at);
CREATE TABLE IF NOT EXISTS agent_task_events (
 task_id uuid NOT NULL REFERENCES agent_tasks(id),
 seq bigint NOT NULL,
 version integer NOT NULL,
 event jsonb NOT NULL,
 PRIMARY KEY(task_id,seq)
);
CREATE TABLE IF NOT EXISTS agent_task_calls (
 task_id uuid NOT NULL REFERENCES agent_tasks(id),
 call_key text NOT NULL,
 record jsonb NOT NULL,
 PRIMARY KEY(task_id,call_key)
);
CREATE TABLE IF NOT EXISTS agent_task_notifications (
 id text PRIMARY KEY,
 username text NOT NULL,
 session_id uuid NOT NULL REFERENCES agent_sessions(id),
 content text NOT NULL,
 enqueued boolean NOT NULL DEFAULT false,
 acknowledged boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
