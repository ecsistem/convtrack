-- 025_admin.sql
-- Adiciona suporte a admin e aprovação de contas.

ALTER TABLE accounts
  ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS status   TEXT    NOT NULL DEFAULT 'approved';
  -- status: 'approved' | 'pending' | 'suspended'
  -- Default 'approved' mantém contas existentes funcionando.

CREATE INDEX IF NOT EXISTS idx_accounts_status ON accounts(status);
