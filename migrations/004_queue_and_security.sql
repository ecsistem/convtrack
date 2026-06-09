-- Jobs que esgotaram todas as tentativas de retry
CREATE TABLE failed_jobs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id      TEXT NOT NULL,
    job_type    TEXT NOT NULL,
    payload     JSONB,
    platform    TEXT,
    project_id  TEXT,
    error_msg   TEXT,
    attempts    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_failed_jobs_project ON failed_jobs(project_id, created_at);

-- Domínios que usaram o tracker sem permissão (detecção de clones)
CREATE TABLE domain_violations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      TEXT NOT NULL,
    project_domain  TEXT NOT NULL,
    request_domain  TEXT NOT NULL,
    ip              TEXT,
    user_agent      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_domain_violations_project ON domain_violations(project_id, created_at);
CREATE INDEX idx_domain_violations_domain  ON domain_violations(request_domain);
