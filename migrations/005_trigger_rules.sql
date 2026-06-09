CREATE TABLE trigger_rules (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,

    -- Tipo do gatilho
    type             TEXT NOT NULL CHECK (type IN ('pageload','click','visibility','scroll','submit')),

    -- Evento a disparar (ex: Purchase, Lead, InitiateCheckout)
    event_name       TEXT NOT NULL,

    -- Filtros opcionais
    url_pattern      TEXT,        -- glob ou "contains:texto"; NULL = qualquer URL
    selector         TEXT,        -- seletor CSS (click, visibility, submit)
    scroll_depth     SMALLINT,    -- 25 | 50 | 75 | 100 (type=scroll)

    -- Payload extra enviado com o evento
    properties       JSONB NOT NULL DEFAULT '{}',

    -- Se TRUE, dispara uma conversão server-side (aciona CAPI/TikTok/Kwai/Google)
    fire_conversion  BOOLEAN NOT NULL DEFAULT FALSE,

    sort_order       SMALLINT NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_trigger_rules_project ON trigger_rules(project_id, enabled, sort_order);
