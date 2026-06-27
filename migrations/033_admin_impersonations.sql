-- Auditoria de "acessar painel do cliente" pelo admin (impersonation).
-- Toda vez que um admin gera um token para entrar como outra conta, fica
-- registrado aqui — quem, quando e em qual conta.
CREATE TABLE IF NOT EXISTS admin_impersonations (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_account_id  UUID        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    target_account_id UUID        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS admin_impersonations_target
    ON admin_impersonations(target_account_id, created_at DESC);
