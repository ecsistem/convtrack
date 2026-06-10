-- Interaction metrics
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS click_count      INT DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS input_count      INT DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS scroll_depth_pct INT DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS rage_clicks      INT DEFAULT 0;
