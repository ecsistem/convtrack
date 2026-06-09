-- Multi-tenant accounts
CREATE TABLE accounts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    email       TEXT UNIQUE NOT NULL,
    plan        TEXT NOT NULL DEFAULT 'starter', -- starter | pro | enterprise
    sessions_quota  INTEGER NOT NULL DEFAULT 10000,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Each account can have multiple sites/projects
CREATE TABLE projects (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    domain      TEXT NOT NULL,
    api_key     TEXT UNIQUE NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_projects_api_key ON projects(api_key);
CREATE INDEX idx_projects_account_id ON projects(account_id);

-- Unique visitors (persistent across sessions)
CREATE TABLE visitors (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    fingerprint TEXT,                -- device fingerprint hash
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_visitors_project_id ON visitors(project_id);

-- Maps hashed emails/phones to visitor_ids for attribution
CREATE TABLE visitor_identifiers (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    visitor_id  UUID NOT NULL REFERENCES visitors(id) ON DELETE CASCADE,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,    -- email | phone
    value_hash  TEXT NOT NULL,    -- SHA-256 of lowercased normalized value
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, value_hash)
);

CREATE INDEX idx_visitor_identifiers_hash ON visitor_identifiers(project_id, value_hash);

-- Individual visit sessions
CREATE TABLE sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    visitor_id      UUID NOT NULL REFERENCES visitors(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_activity   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    landing_page    TEXT,
    referrer        TEXT,
    user_agent      TEXT,
    ip              TEXT,
    country         TEXT,
    city            TEXT,
    device          TEXT,
    browser         TEXT,
    os              TEXT
);

CREATE INDEX idx_sessions_visitor_id ON sessions(visitor_id);
CREATE INDEX idx_sessions_project_id ON sessions(project_id);
CREATE INDEX idx_sessions_started_at ON sessions(started_at);

-- UTM and click ID attribution per session
CREATE TABLE attributions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    utm_source      TEXT,
    utm_medium      TEXT,
    utm_campaign    TEXT,
    utm_content     TEXT,
    utm_term        TEXT,
    fbclid          TEXT,
    gclid           TEXT,
    ttclid          TEXT,
    fbp             TEXT,   -- _fbp cookie
    fbc             TEXT,   -- _fbc cookie (or derived from fbclid)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_attributions_session ON attributions(session_id);
CREATE INDEX idx_attributions_project ON attributions(project_id);
CREATE INDEX idx_attributions_utm_campaign ON attributions(project_id, utm_campaign);

-- Custom events (pageview, scroll, click, add_to_cart, etc.)
CREATE TABLE events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    properties  JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_events_session ON events(session_id);
CREATE INDEX idx_events_project_name ON events(project_id, name);
CREATE INDEX idx_events_created_at ON events(created_at);

-- Completed conversions (purchases, leads)
CREATE TABLE conversions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id      UUID REFERENCES sessions(id),
    external_id     TEXT,                  -- order ID from payment platform
    event_name      TEXT NOT NULL,         -- Purchase | Lead | InitiateCheckout
    value           NUMERIC(12,2),
    currency        TEXT NOT NULL DEFAULT 'BRL',
    email_hash      TEXT,
    phone_hash      TEXT,
    platform        TEXT,                  -- hotmart | kiwify | eduzz | generic
    raw_payload     JSONB,
    attributed      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_conversions_project ON conversions(project_id);
CREATE INDEX idx_conversions_session ON conversions(session_id);
CREATE INDEX idx_conversions_created_at ON conversions(created_at);
CREATE INDEX idx_conversions_email_hash ON conversions(project_id, email_hash);

-- Integration settings per project
CREATE TABLE integration_settings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    platform        TEXT NOT NULL,  -- meta | google | tiktok
    enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    config          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(project_id, platform)
);

-- Log of all CAPI/enhanced conversion calls
CREATE TABLE integration_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    conversion_id   UUID REFERENCES conversions(id),
    platform        TEXT NOT NULL,
    event_name      TEXT NOT NULL,
    request_body    JSONB,
    response_status INTEGER,
    response_body   TEXT,
    success         BOOLEAN,
    error_msg       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_integration_logs_project ON integration_logs(project_id, created_at);
CREATE INDEX idx_integration_logs_conversion ON integration_logs(conversion_id);

-- Session replay storage reference (actual data in R2/S3)
CREATE TABLE replays (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    storage_key     TEXT NOT NULL,  -- R2/S3 object key
    duration_ms     INTEGER,
    trigger_event   TEXT,           -- which event triggered recording
    size_bytes      INTEGER,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_replays_session ON replays(session_id);
CREATE INDEX idx_replays_project ON replays(project_id, created_at);
