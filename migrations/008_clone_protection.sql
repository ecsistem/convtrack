-- 008_clone_protection.sql
-- Adiciona flag de proteção contra clone aos projetos.

ALTER TABLE projects
  ADD COLUMN IF NOT EXISTS clone_protection BOOLEAN NOT NULL DEFAULT FALSE;
