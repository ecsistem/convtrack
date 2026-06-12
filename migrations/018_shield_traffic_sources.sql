-- Fontes de tráfego: filtragem por origem (Meta, TikTok, Kwai, Google, Direto).
-- Quando block_direct=true, apenas visitas cujo referer/utm_source corresponda
-- a uma das allowed_sources são encaminhadas para a money URL.
-- Tráfego direto (sem referrer reconhecido) vai para safe_url.

ALTER TABLE shield_configs
  ADD COLUMN IF NOT EXISTS block_direct     BOOLEAN  NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS allowed_sources  TEXT[]   NOT NULL DEFAULT '{}';
