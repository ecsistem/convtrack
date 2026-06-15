-- 024_campaign_origin_only.sql
-- origin_only: quando true, a campanha valida a ORIGEM real do clique (Referer)
-- e ignora o utm_source (que pode ser forjado). Visitantes cuja origem não
-- bate com as plataformas da campanha vão para a safe_url.

ALTER TABLE shield_campaigns
  ADD COLUMN IF NOT EXISTS origin_only BOOLEAN NOT NULL DEFAULT false;
