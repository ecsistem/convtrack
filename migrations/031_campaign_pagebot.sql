-- Pagebot: interstitial anti-bot na Página White (estilo "Checking your browser…").
-- Segura o tráfego até uma ação do usuário antes de liberar a Página White.
-- Valores: none|cloudflare
ALTER TABLE shield_campaigns ADD COLUMN IF NOT EXISTS pagebot TEXT NOT NULL DEFAULT 'none';
