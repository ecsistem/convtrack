-- Allowlist de sistema operacional para o Shield (iOS, Android, Windows, macOS).
-- Array vazio = sem restrição (todos os SOs passam).
ALTER TABLE shield_configs ADD COLUMN IF NOT EXISTS os_allowed TEXT[] NOT NULL DEFAULT '{}';
