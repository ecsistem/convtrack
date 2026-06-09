-- Session enrichment metrics
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS screen_width    INT     DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS screen_height   INT     DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS timezone        TEXT    DEFAULT '';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS language        TEXT    DEFAULT '';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS duration_seconds INT    DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS page_count      INT     DEFAULT 1;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS exit_page       TEXT    DEFAULT '';
