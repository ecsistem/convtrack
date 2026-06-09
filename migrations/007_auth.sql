-- 007_auth.sql
-- Adiciona autenticação de usuários com JWT + refresh tokens.

-- Campos de autenticação na tabela accounts
ALTER TABLE accounts
  ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT FALSE;

-- Email único por conta
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_email_unique ON accounts(lower(email));

-- Tabela de refresh tokens (revogação e rotação)
CREATE TABLE IF NOT EXISTS auth_tokens (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,  -- SHA-256 do token para não armazenar em texto claro
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_auth_tokens_account ON auth_tokens(account_id);
