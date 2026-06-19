ALTER TABLE accounts
  ADD COLUMN IF NOT EXISTS is_affiliate  BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS affiliate_ref TEXT;

CREATE TABLE IF NOT EXISTS affiliates (
  id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id     UUID        NOT NULL UNIQUE REFERENCES accounts(id) ON DELETE CASCADE,
  code           TEXT        NOT NULL UNIQUE,
  commission_pct INT         NOT NULL DEFAULT 30,
  status         TEXT        NOT NULL DEFAULT 'active',
  total_earned   NUMERIC(10,2) NOT NULL DEFAULT 0,
  paid_out       NUMERIC(10,2) NOT NULL DEFAULT 0,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS affiliate_referrals (
  id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  affiliate_id         UUID        NOT NULL REFERENCES affiliates(id) ON DELETE CASCADE,
  referred_account_id  UUID        NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  plan                 TEXT        NOT NULL,
  amount               NUMERIC(10,2) NOT NULL,
  commission           NUMERIC(10,2) NOT NULL,
  paid                 BOOLEAN     NOT NULL DEFAULT FALSE,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(affiliate_id, referred_account_id)
);

CREATE INDEX IF NOT EXISTS idx_affiliates_code       ON affiliates(code);
CREATE INDEX IF NOT EXISTS idx_affiliates_account    ON affiliates(account_id);
CREATE INDEX IF NOT EXISTS idx_referrals_affiliate   ON affiliate_referrals(affiliate_id);
CREATE INDEX IF NOT EXISTS idx_accounts_affiliate_ref ON accounts(affiliate_ref);
