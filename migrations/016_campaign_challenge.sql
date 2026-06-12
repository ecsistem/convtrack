-- Migration 016: challenge mode + platform + secret key para shield_campaigns

ALTER TABLE shield_campaigns
  ADD COLUMN IF NOT EXISTS platform        TEXT    NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS challenge_mode  TEXT    NOT NULL DEFAULT 'redirect',
  ADD COLUMN IF NOT EXISTS require_key     BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS access_key      TEXT    NOT NULL DEFAULT '';
