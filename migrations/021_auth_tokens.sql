-- 021_auth_tokens.sql — tokens para reset de senha e verificação de email.

CREATE TABLE IF NOT EXISTS password_reset_tokens (
    token_hash  TEXT PRIMARY KEY,             -- sha256 do token enviado por email
    account_id  UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pwreset_account ON password_reset_tokens (account_id);
CREATE INDEX IF NOT EXISTS idx_pwreset_expires ON password_reset_tokens (expires_at);

CREATE TABLE IF NOT EXISTS email_verification_tokens (
    token_hash  TEXT PRIMARY KEY,
    account_id  UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_emailverify_account ON email_verification_tokens (account_id);
