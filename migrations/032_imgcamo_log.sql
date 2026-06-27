-- Histórico de imagens camufladas (criativos), para o admin poder visualizar
-- o que cada conta/projeto gerou. O arquivo em si fica em disco
-- (IMGCAMO_DIR); aqui guardamos só os metadados + caminho.
CREATE TABLE IF NOT EXISTS imgcamo_log (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    filename     TEXT        NOT NULL DEFAULT '',
    technique    TEXT        NOT NULL DEFAULT '',
    epsilon      INT         NOT NULL DEFAULT 0,
    mime_type    TEXT        NOT NULL DEFAULT '',
    size_bytes   INT         NOT NULL DEFAULT 0,
    storage_path TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS imgcamo_log_project_created
    ON imgcamo_log(project_id, created_at DESC);
