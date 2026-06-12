-- 022_campaign_clickid_reviewers.sql
-- require_clickid: exige o click-id da plataforma da campanha (fbclid/gclid/
-- ttclid/clickid). Visitantes sem o parâmetro vão para a safe_url — pega
-- revisores de anúncio que abrem o link sem o click pago real.

ALTER TABLE shield_campaigns
  ADD COLUMN IF NOT EXISTS require_clickid BOOLEAN NOT NULL DEFAULT false;
