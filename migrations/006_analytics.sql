-- 006_analytics.sql
-- Adiciona campos financeiros às conversões, tabela de custos de anúncio e configurações do projeto.

-- Novos campos na tabela conversions
ALTER TABLE conversions
  ADD COLUMN IF NOT EXISTS payment_method TEXT,        -- cartao | pix | boleto
  ADD COLUMN IF NOT EXISTS status         TEXT NOT NULL DEFAULT 'approved',  -- approved | pending | refunded | chargeback
  ADD COLUMN IF NOT EXISTS product_name   TEXT,
  ADD COLUMN IF NOT EXISTS product_cost   NUMERIC(12,2) NOT NULL DEFAULT 0;

-- Índices para queries de analytics
CREATE INDEX IF NOT EXISTS idx_conversions_status          ON conversions(project_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_conversions_payment_method  ON conversions(project_id, payment_method, status);
CREATE INDEX IF NOT EXISTS idx_conversions_product_name    ON conversions(project_id, product_name);

-- Tabela de custos de anúncio (input manual por dia/plataforma)
CREATE TABLE IF NOT EXISTS ad_costs (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  date          DATE NOT NULL,
  ad_account_id TEXT,
  platform      TEXT,         -- meta | google | tiktok | kwai | organic
  utm_source    TEXT,
  utm_campaign  TEXT,
  amount        NUMERIC(12,2) NOT NULL,
  currency      TEXT NOT NULL DEFAULT 'BRL',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ad_costs_project_date ON ad_costs(project_id, date);

-- Configurações financeiras por projeto
CREATE TABLE IF NOT EXISTS project_settings (
  project_id                  UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  tax_rate                    NUMERIC(6,4) NOT NULL DEFAULT 0,    -- ex: 0.15 = 15%
  additional_expenses_monthly NUMERIC(12,2) NOT NULL DEFAULT 0,  -- despesas fixas mensais
  product_cost_default        NUMERIC(12,2) NOT NULL DEFAULT 0,  -- custo padrão do produto
  updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
