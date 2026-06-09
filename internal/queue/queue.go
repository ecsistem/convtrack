// Package queue implementa uma fila de jobs assíncrona sobre Redis com
// retry exponencial e suporte a jobs atrasados (delayed).
//
// Estruturas Redis:
//   - List  "queue:pending"   → jobs prontos para executar (LPUSH / BRPOP)
//   - ZSet  "queue:delayed"   → jobs aguardando retry (score = unix de execução)
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	keyPending = "queue:pending"
	keyDelayed = "queue:delayed"

	MaxAttempts = 5
)

// retryDelays define o backoff exponencial: 30s, 1m, 2m, 4m, 8m
var retryDelays = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	2 * time.Minute,
	4 * time.Minute,
	8 * time.Minute,
}

// JobType identifica o tipo de trabalho a executar
type JobType string

const (
	JobFireIntegration JobType = "fire_integration"
)

// Job representa uma unidade de trabalho enfileirada
type Job struct {
	ID          string  `json:"id"`
	Type        JobType `json:"type"`
	Payload     []byte  `json:"payload"`
	Attempts    int     `json:"attempts"`
	MaxAttempts int     `json:"max_attempts"`
	CreatedAt   int64   `json:"created_at"`
	// Contexto para logs
	ProjectID string `json:"project_id"`
	Platform  string `json:"platform"`
}

// FireIntegrationPayload é o payload do job fire_integration
type FireIntegrationPayload struct {
	ConversionID string `json:"conversion_id"`
	ProjectID    string `json:"project_id"`
	Platform     string `json:"platform"` // meta | google | tiktok | kwai
}

type Queue struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Queue {
	return &Queue{rdb: rdb}
}

// Enqueue adiciona um job à fila de execução imediata
func (q *Queue) Enqueue(ctx context.Context, job *Job) error {
	if job.ID == "" {
		job.ID = uuid.New().String()
	}
	if job.MaxAttempts == 0 {
		job.MaxAttempts = MaxAttempts
	}
	job.CreatedAt = time.Now().Unix()

	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("queue: marshal job: %w", err)
	}
	return q.rdb.LPush(ctx, keyPending, data).Err()
}

// EnqueueFireIntegration cria e enfileira um job para disparar uma integração
func (q *Queue) EnqueueFireIntegration(ctx context.Context, conversionID, projectID, platform string) error {
	payload, _ := json.Marshal(FireIntegrationPayload{
		ConversionID: conversionID,
		ProjectID:    projectID,
		Platform:     platform,
	})
	return q.Enqueue(ctx, &Job{
		Type:      JobFireIntegration,
		Payload:   payload,
		ProjectID: projectID,
		Platform:  platform,
	})
}

// Dequeue bloqueia até ter um job disponível (timeout 2s para permitir shutdown limpo)
func (q *Queue) Dequeue(ctx context.Context) (*Job, error) {
	result, err := q.rdb.BRPop(ctx, 2*time.Second, keyPending).Result()
	if err == redis.Nil || err == context.DeadlineExceeded {
		return nil, nil // timeout normal — sem jobs
	}
	if err != nil {
		return nil, fmt.Errorf("queue: brpop: %w", err)
	}
	// result = ["queue:pending", "<json>"]
	var job Job
	if err := json.Unmarshal([]byte(result[1]), &job); err != nil {
		return nil, fmt.Errorf("queue: unmarshal job: %w", err)
	}
	return &job, nil
}

// Schedule adiciona o job ao sorted set de delayed jobs com score = executa_em
func (q *Queue) Schedule(ctx context.Context, job *Job, delay time.Duration) error {
	job.Attempts++
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("queue: marshal delayed job: %w", err)
	}
	score := float64(time.Now().Add(delay).Unix())
	return q.rdb.ZAdd(ctx, keyDelayed, redis.Z{
		Score:  score,
		Member: string(data),
	}).Err()
}

// RetryDelay retorna o delay para a tentativa N (0-indexed)
func RetryDelay(attempt int) time.Duration {
	if attempt >= len(retryDelays) {
		return retryDelays[len(retryDelays)-1]
	}
	return retryDelays[attempt]
}

// ReapDelayed move para a fila pendente todos os jobs do sorted set cujo score <= now.
// Deve ser chamado periodicamente pelo worker (a cada ~5s).
//
// Script Lua garante atomicidade: lê + deleta + re-enfileira sem race condition.
var reapScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local jobs = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now)
if #jobs == 0 then return 0 end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
for _, job in ipairs(jobs) do
    redis.call('LPUSH', KEYS[2], job)
end
return #jobs
`)

func (q *Queue) ReapDelayed(ctx context.Context) (int, error) {
	result, err := reapScript.Run(ctx, q.rdb,
		[]string{keyDelayed, keyPending},
		time.Now().Unix(),
	).Int()
	if err != nil && err != redis.Nil {
		return 0, fmt.Errorf("queue: reap delayed: %w", err)
	}
	return result, nil
}

// Len retorna quantos jobs estão na fila pendente
func (q *Queue) Len(ctx context.Context) (int64, error) {
	return q.rdb.LLen(ctx, keyPending).Result()
}
