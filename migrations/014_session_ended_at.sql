-- Migration 014: ended_at column for sessions
-- Marks when a session was explicitly closed by the tracker (beforeunload / pagehide / visibilitychange hidden)

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS ended_at TIMESTAMPTZ;

-- Retroactively estimate ended_at for sessions that have a duration
UPDATE sessions
SET ended_at = started_at + (duration_seconds * interval '1 second')
WHERE ended_at IS NULL AND duration_seconds > 0;

-- Index to find active sessions quickly (ended_at IS NULL)
CREATE INDEX IF NOT EXISTS idx_sessions_ended_at ON sessions (ended_at)
WHERE ended_at IS NULL;
