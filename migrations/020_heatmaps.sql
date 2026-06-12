-- 020_heatmaps.sql — armazenamento de cliques para heatmap.
-- Cada clique guarda a posição relativa (0-1) ao documento para permitir
-- reconstruir o heatmap independente da resolução do visitante.

CREATE TABLE IF NOT EXISTS heatmap_clicks (
    id          BIGSERIAL PRIMARY KEY,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id  UUID,
    url_path    TEXT NOT NULL,
    x           INTEGER NOT NULL,          -- posição absoluta no documento (pageX)
    y           INTEGER NOT NULL,          -- posição absoluta no documento (pageY)
    xp          REAL NOT NULL,             -- posição relativa horizontal 0-1
    yp          REAL NOT NULL,             -- posição relativa vertical 0-1
    vw          INTEGER,                   -- viewport width
    vh          INTEGER,                   -- viewport height
    dw          INTEGER,                   -- document width
    dh          INTEGER,                   -- document height
    selector    TEXT,                      -- CSS selector do elemento clicado
    tag         TEXT,                      -- tag HTML (button, a, div…)
    text        TEXT,                      -- texto do elemento (até 40 chars)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_heatmap_project_url
    ON heatmap_clicks (project_id, url_path, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_heatmap_created_at
    ON heatmap_clicks (created_at);
