package handlers

import (
	"bufio"
	"fmt"
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

type TestEventsHandler struct {
	rdb *redis.Client
}

func NewTestEvents(rdb *redis.Client) *TestEventsHandler {
	return &TestEventsHandler{rdb: rdb}
}

// GET /v1/test-events/stream
// Abre um Server-Sent Events stream. O dashboard mantém a conexão aberta e
// recebe eventos em tempo real sempre que o worker processa um job para o projeto.
//
// Cada mensagem SSE tem o formato:
//
//	data: {"platform":"meta","event_name":"Purchase","success":true,"attempt":1,...}
func (h *TestEventsHandler) Stream(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	channel := fmt.Sprintf("test_events:%s", project.ID)

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no") // desabilita buffer do nginx

	// heartbeat a cada 30s para manter a conexão viva
	heartbeatTicker := time.NewTicker(30 * time.Second)

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer heartbeatTicker.Stop()

		// Cria um subscriber Redis dedicado para esta conexão
		pubsub := h.rdb.Subscribe(c.Context(), channel)
		defer pubsub.Close()

		redisCh := pubsub.Channel()

		// Avisa o cliente que o stream está pronto
		fmt.Fprintf(w, "data: {\"connected\":true,\"project_id\":%q}\n\n", project.ID)
		_ = w.Flush()

		for {
			select {
			case msg, ok := <-redisCh:
				if !ok {
					return // canal fechado
				}
				fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
				if err := w.Flush(); err != nil {
					return // cliente desconectou
				}

			case <-heartbeatTicker.C:
				// SSE comment como heartbeat (não gera evento no JS)
				fmt.Fprintf(w, ": heartbeat\n\n")
				if err := w.Flush(); err != nil {
					return
				}

			case <-c.Context().Done():
				return
			}
		}
	})

	return nil
}
