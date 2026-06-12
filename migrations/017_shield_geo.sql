-- Adiciona dados de geolocalização (cidade + lat/lon) às visitas do Shield.
-- Obtidos via ip-api.com no momento da visita, junto com o countryCode já existente.

ALTER TABLE shield_visits
  ADD COLUMN IF NOT EXISTS city TEXT             NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS lat  DOUBLE PRECISION NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS lon  DOUBLE PRECISION NOT NULL DEFAULT 0;

-- Índice para a query de geo stats (agrupamento por cidade)
CREATE INDEX IF NOT EXISTS idx_shield_visits_city ON shield_visits (project_id, city, created_at DESC);
