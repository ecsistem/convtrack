-- Migration 015: remove FK constraint from events.session_id
-- Fixes race condition: pageload rules fire /collect/event before /collect/session
-- is committed to the DB → FK violation causes silent INSERT failure.
-- The column stays NOT NULL (we always pass a session_id), but the FK enforcement
-- is removed so a brief out-of-order arrival never drops the event.
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_session_id_fkey;
