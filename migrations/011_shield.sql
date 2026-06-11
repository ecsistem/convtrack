-- Shield: configuration & logs per project
-- Migration 011

CREATE TABLE IF NOT EXISTS shield_configs (
    project_id          UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled             BOOLEAN   NOT NULL DEFAULT false,

    -- Automação / bots
    block_bots          BOOLEAN   NOT NULL DEFAULT true,
    block_headless      BOOLEAN   NOT NULL DEFAULT true,
    block_spy_tools     BOOLEAN   NOT NULL DEFAULT true,

    -- Rede
    block_vpn           BOOLEAN   NOT NULL DEFAULT false,
    block_datacenter    BOOLEAN   NOT NULL DEFAULT false,

    -- Anti-DevTools (cliente)
    anti_devtools       BOOLEAN   NOT NULL DEFAULT false,

    -- GEO
    geo_mode            TEXT      NOT NULL DEFAULT 'disabled',  -- 'disabled' | 'allowlist' | 'blocklist'
    geo_countries       TEXT[]    NOT NULL DEFAULT '{}',

    -- Dispositivo
    device_filter       TEXT      NOT NULL DEFAULT 'all',       -- 'all' | 'mobile' | 'desktop'

    -- Redirecionamento
    redirect_url        TEXT      NOT NULL DEFAULT '',  -- destino de visitantes bloqueados
    primary_url         TEXT      NOT NULL DEFAULT '',  -- destino de visitantes legítimos
    fallback_urls       TEXT[]    NOT NULL DEFAULT '{}',

    -- Bloqueios customizados
    blocked_ips         TEXT[]    NOT NULL DEFAULT '{}',

    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS shield_logs (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    ip          TEXT        NOT NULL DEFAULT '',
    user_agent  TEXT        NOT NULL DEFAULT '',
    country     TEXT        NOT NULL DEFAULT '',
    device      TEXT        NOT NULL DEFAULT '',
    -- reason: bot | spy_tool | headless | vpn | datacenter | geo | device | ip_blocked | devtools | webdriver
    reason      TEXT        NOT NULL DEFAULT '',
    -- action: blocked | redirected | allowed
    action      TEXT        NOT NULL DEFAULT '',
    redirect_to TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS shield_logs_project_created
    ON shield_logs(project_id, created_at DESC);
