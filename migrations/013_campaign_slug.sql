-- Migration 013: slug único por campanha + índice para lookup por slug
ALTER TABLE shield_campaigns
    ADD COLUMN IF NOT EXISTS slug TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS shield_campaigns_slug
    ON shield_campaigns(slug) WHERE slug <> '';
