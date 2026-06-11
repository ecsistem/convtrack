-- Shield Advanced: campaigns, domains, fingerprints, visits, webhooks
-- Migration 012

-- Campanhas: safe_url (bots/revisores) vs money_url (humanos legítimos) + A/B split
CREATE TABLE IF NOT EXISTS shield_campaigns (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    safe_url    TEXT        NOT NULL DEFAULT '',
    money_url   TEXT        NOT NULL DEFAULT '',
    split_pct   SMALLINT    NOT NULL DEFAULT 100,  -- % do tráfego limpo → money_url
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Domínios: mapeamento hostname → campanha (para proxy reverso)
CREATE TABLE IF NOT EXISTS shield_domains (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    campaign_id UUID        NOT NULL REFERENCES shield_campaigns(id) ON DELETE CASCADE,
    domain      TEXT        NOT NULL UNIQUE,
    ssl_enabled BOOLEAN     NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS shield_domains_domain ON shield_domains(domain);

-- Fingerprints: sinais coletados pelo shield-fp.js (client-side)
CREATE TABLE IF NOT EXISTS shield_fingerprints (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_hash    TEXT        NOT NULL DEFAULT '',
    canvas_hash     TEXT        NOT NULL DEFAULT '',
    webgl_vendor    TEXT        NOT NULL DEFAULT '',
    webgl_renderer  TEXT        NOT NULL DEFAULT '',
    webgl_hash      TEXT        NOT NULL DEFAULT '',
    audio_hash      TEXT        NOT NULL DEFAULT '',
    screen_width    INT         NOT NULL DEFAULT 0,
    screen_height   INT         NOT NULL DEFAULT 0,
    color_depth     INT         NOT NULL DEFAULT 0,
    pixel_ratio     FLOAT       NOT NULL DEFAULT 0,
    timezone        TEXT        NOT NULL DEFAULT '',
    language        TEXT        NOT NULL DEFAULT '',
    platform        TEXT        NOT NULL DEFAULT '',
    cpu_cores       INT         NOT NULL DEFAULT 0,
    memory_gb       INT         NOT NULL DEFAULT 0,
    touch_points    INT         NOT NULL DEFAULT 0,
    webrtc_ips      TEXT[]      NOT NULL DEFAULT '{}',
    fonts_hash      TEXT        NOT NULL DEFAULT '',
    plugins         INT         NOT NULL DEFAULT 0,
    combined_hash   TEXT        NOT NULL DEFAULT '',
    bot_score       FLOAT       NOT NULL DEFAULT 0,
    signals         TEXT[]      NOT NULL DEFAULT '{}',
    is_bot          BOOLEAN     NOT NULL DEFAULT false,
    ip              TEXT        NOT NULL DEFAULT '',
    user_agent      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS shield_fingerprints_project_created
    ON shield_fingerprints(project_id, created_at DESC);

-- Visitas: registro completo de cada decisão do Shield (humano + bot)
CREATE TABLE IF NOT EXISTS shield_visits (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    campaign_id     UUID        REFERENCES shield_campaigns(id) ON DELETE SET NULL,
    fingerprint_id  UUID        REFERENCES shield_fingerprints(id) ON DELETE SET NULL,
    domain          TEXT        NOT NULL DEFAULT '',
    ip              TEXT        NOT NULL DEFAULT '',
    user_agent      TEXT        NOT NULL DEFAULT '',
    country         TEXT        NOT NULL DEFAULT '',
    device          TEXT        NOT NULL DEFAULT '',
    is_bot          BOOLEAN     NOT NULL DEFAULT false,
    bot_score       FLOAT       NOT NULL DEFAULT 0,
    signals         TEXT[]      NOT NULL DEFAULT '{}',
    action          TEXT        NOT NULL DEFAULT '',  -- 'money' | 'safe' | 'blocked' | 'redirected'
    dest_url        TEXT        NOT NULL DEFAULT '',
    process_ms      INT         NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS shield_visits_project_created
    ON shield_visits(project_id, created_at DESC);

-- Webhooks: Telegram / Discord / HTTP customizado
CREATE TABLE IF NOT EXISTS shield_webhooks (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    -- 'telegram' | 'discord' | 'custom'
    type        TEXT        NOT NULL,
    url         TEXT        NOT NULL DEFAULT '',
    token       TEXT        NOT NULL DEFAULT '',    -- telegram: bot token
    chat_id     TEXT        NOT NULL DEFAULT '',    -- telegram: chat_id
    -- eventos: 'bot_detected' | 'visit' | 'conversion'
    events      TEXT[]      NOT NULL DEFAULT '{bot_detected}',
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
