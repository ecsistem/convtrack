-- 023_campaign_platforms.sql
-- Permite múltiplas fontes de tráfego por campanha. A coluna `platform`
-- (singular) é mantida para compatibilidade e passa a refletir a primeira
-- plataforma de `platforms`.

ALTER TABLE shield_campaigns
  ADD COLUMN IF NOT EXISTS platforms TEXT[] NOT NULL DEFAULT '{}';

-- Backfill: campanhas existentes com platform definido viram array de 1 item.
UPDATE shield_campaigns
SET platforms = ARRAY[platform]
WHERE platform IS NOT NULL AND platform <> ''
  AND (platforms IS NULL OR platforms = '{}');
