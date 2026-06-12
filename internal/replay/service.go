package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ecsistem/convtrack/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	// Prefixo das chaves Redis para buffer de eventos
	redisPrefix = "replay:buf:"
	// TTL do buffer: se não chegar nada em 30 min, expira e perde (sessão abandonada)
	bufferTTL = 30 * time.Minute
)

type Service struct {
	db    *pgxpool.Pool
	redis *redis.Client
	s3    *storage.S3Client
}

func New(db *pgxpool.Pool, rdb *redis.Client, s3 *storage.S3Client) *Service {
	return &Service{db: db, redis: rdb, s3: s3}
}

// AppendEvents adiciona eventos rrweb ao buffer Redis da sessão.
// O flush pro S3 é controlado pelo frontend (timer periódico de 60s + beforeunload).
func (s *Service) AppendEvents(ctx context.Context, projectID, sessionID uuid.UUID, events []json.RawMessage, triggerEvent string) error {
	key := redisPrefix + sessionID.String()

	pipe := s.redis.Pipeline()
	for _, e := range events {
		pipe.RPush(ctx, key, []byte(e))
	}
	pipe.Expire(ctx, key, bufferTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("replay: redis append: %w", err)
	}

	return nil
}

// FlushToS3 lê todos os eventos acumulados do buffer Redis, comprime e sobe pro S3.
// NÃO apaga o buffer — cada flush regrava o replay completo (todos os eventos da sessão).
// O Redis expira naturalmente pelo TTL (30 min de inatividade).
// Isso garante que o replay no S3 sempre contenha a gravação inteira,
// mesmo com múltiplos flushes periódicos durante a mesma sessão.
func (s *Service) FlushToS3(ctx context.Context, projectID, sessionID uuid.UUID, triggerEvent string) error {
	key := redisPrefix + sessionID.String()

	// Lê TODOS os eventos acumulados (desde o início da sessão)
	raw, err := s.redis.LRange(ctx, key, 0, -1).Result()
	if err != nil || len(raw) == 0 {
		return nil
	}

	// Renova o TTL para não expirar durante sessão ativa
	_ = s.redis.Expire(ctx, key, bufferTTL)

	// Monta array JSON: [ event1, event2, ... ]
	payload, err := buildJSONArray(raw)
	if err != nil {
		return fmt.Errorf("replay: build json: %w", err)
	}

	// Chave no S3: replays/{projectID}/{sessionID}.json.gz
	s3Key := fmt.Sprintf("replays/%s/%s.json.gz", projectID, sessionID)

	if err := s.s3.UploadReplay(ctx, s3Key, payload); err != nil {
		return fmt.Errorf("replay: s3 upload: %w", err)
	}

	// Salva/atualiza referência no banco (ON CONFLICT atualiza o registro existente)
	if err := s.upsertReplayRecord(ctx, sessionID, projectID, s3Key, len(raw), triggerEvent); err != nil {
		fmt.Printf("replay: save record error: %v\n", err)
	}

	return nil
}

// GetPresignedURL retorna uma URL pré-assinada para o frontend reproduzir o replay.
func (s *Service) GetPresignedURL(ctx context.Context, sessionID uuid.UUID) (string, error) {
	var storageKey string
	err := s.db.QueryRow(ctx,
		`SELECT storage_key FROM replays WHERE session_id = $1 ORDER BY created_at DESC LIMIT 1`,
		sessionID,
	).Scan(&storageKey)
	if err != nil {
		return "", fmt.Errorf("replay: not found for session %s", sessionID)
	}

	url, err := s.s3.PresignedURL(ctx, storageKey, time.Hour)
	if err != nil {
		return "", fmt.Errorf("replay: presign: %w", err)
	}
	return url, nil
}

// HasReplay informa se existe replay gravado para a sessão
func (s *Service) HasReplay(ctx context.Context, sessionID uuid.UUID) bool {
	var count int
	_ = s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM replays WHERE session_id = $1`, sessionID,
	).Scan(&count)
	return count > 0
}

func (s *Service) upsertReplayRecord(ctx context.Context, sessionID, projectID uuid.UUID, s3Key string, eventCount int, triggerEvent string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO replays (session_id, project_id, storage_key, trigger_event)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (session_id) DO UPDATE
			SET storage_key   = EXCLUDED.storage_key,
			    trigger_event = EXCLUDED.trigger_event,
			    created_at    = NOW()`,
		sessionID, projectID, s3Key, triggerEvent,
	)
	return err
}

// buildJSONArray junta os eventos brutos em um array JSON válido de forma eficiente
func buildJSONArray(items []string) ([]byte, error) {
	// Estima capacidade: cada item tem ~200 bytes em média
	buf := make([]byte, 0, len(items)*200+2)
	buf = append(buf, '[')
	for i, item := range items {
		buf = append(buf, []byte(item)...)
		if i < len(items)-1 {
			buf = append(buf, ',')
		}
	}
	buf = append(buf, ']')

	// Valida que é JSON válido
	if !json.Valid(buf) {
		return nil, fmt.Errorf("invalid json in replay buffer")
	}
	return buf, nil
}
