-- This file is applied only to the dedicated memory database. Conversation
-- processes receive no write paths to it; its rows are the canonical source
-- for long-term user memory, with Storage as a derived materialization.
CREATE TABLE IF NOT EXISTS user_long_term_memories (
 id uuid PRIMARY KEY,
 username text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('agent', 'memory', 'experiment', 'error', 'preference', 'style', 'prompt', 'constraint')),
 content text NOT NULL CHECK (char_length(content) BETWEEN 1 AND 4000),
 tags jsonb NOT NULL DEFAULT '[]',
 importance double precision NOT NULL CHECK (importance >= 0 AND importance <= 1),
 confidence double precision NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
 source_sessions jsonb NOT NULL DEFAULT '[]',
 source_turn_ids jsonb NOT NULL DEFAULT '[]',
 call_count bigint NOT NULL DEFAULT 0 CHECK (call_count >= 0),
 first_seen_at timestamptz NOT NULL,
 last_seen_at timestamptz NOT NULL,
 last_accessed_at timestamptz,
 expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 archived_at timestamptz,
 profile_slot smallint,
 version integer NOT NULL DEFAULT 1 CHECK (version > 0),
 state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'archived')),
 superseded_by uuid,
 CHECK ((state = 'active' AND profile_slot BETWEEN 1 AND 5) OR (state = 'archived' AND profile_slot IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS user_long_term_memories_active_slot
 ON user_long_term_memories(username, profile_slot) WHERE state = 'active';
CREATE INDEX IF NOT EXISTS user_long_term_memories_recall
 ON user_long_term_memories(username, state, last_seen_at DESC, call_count DESC);
ALTER TABLE user_long_term_memories ADD COLUMN IF NOT EXISTS superseded_by uuid;

CREATE TABLE IF NOT EXISTS user_memory_checkpoints (
 username text PRIMARY KEY,
 processed_through timestamptz NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
