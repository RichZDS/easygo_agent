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
