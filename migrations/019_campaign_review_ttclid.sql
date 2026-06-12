-- Campos por campanha: modo "Em análise" e validação de TikTok Click ID.
--
-- under_review:   quando true, TODOS os visitantes veem a safe URL (white page).
--                 Usar durante revisão de anúncio sem expor a oferta real.
-- require_ttclid: quando true (só faz sentido em campanhas TikTok), exige o
--                 parâmetro ?ttclid= na URL. Visitantes sem o parâmetro vão para safe URL.

ALTER TABLE shield_campaigns
  ADD COLUMN IF NOT EXISTS under_review    BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS require_ttclid  BOOLEAN NOT NULL DEFAULT false;
