CREATE TABLE IF NOT EXISTS subscriptions (
  id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id         UUID        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  plan               TEXT        NOT NULL,
  status             TEXT        NOT NULL DEFAULT 'pending', -- pending|active|cancelled|expired
  provider           TEXT        NOT NULL DEFAULT 'pixup',
  external_id        TEXT,
  current_period_end TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_subscriptions_account    ON subscriptions(account_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_external   ON subscriptions(external_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_status     ON subscriptions(status);
