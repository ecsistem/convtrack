-- Garante que só existe um registro de replay por sessão (upsert funciona corretamente)
ALTER TABLE replays ADD CONSTRAINT replays_session_id_unique UNIQUE (session_id);
