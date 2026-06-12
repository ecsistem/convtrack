// Package live publica eventos de tempo real no Redis para o dashboard
// consumir via Server-Sent Events (canal live:{projectID}).
package live

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Event é a mensagem publicada no canal live de um projeto.
type Event struct {
	Type  string  `json:"type"`            // "session" | "conversion" | "lead"
	Value float64 `json:"value,omitempty"` // valor monetário (conversões)
	Label string  `json:"label,omitempty"` // descrição curta (campanha, plataforma…)
}

func channel(projectID uuid.UUID) string {
	return fmt.Sprintf("live:%s", projectID)
}

// Publish envia um evento ao canal do projeto. Falhas são ignoradas
// (tempo real é best-effort, nunca deve quebrar o fluxo principal).
func Publish(ctx context.Context, rdb *redis.Client, projectID uuid.UUID, evt Event) {
	if rdb == nil {
		return
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	_ = rdb.Publish(ctx, channel(projectID), string(data))
}

// Channel retorna o nome do canal Redis de um projeto (usado pelo SSE handler).
func Channel(projectID uuid.UUID) string {
	return channel(projectID)
}
