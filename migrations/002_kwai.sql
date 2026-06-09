-- Add Kwai click ID to attributions
ALTER TABLE attributions ADD COLUMN IF NOT EXISTS kwclid TEXT;
