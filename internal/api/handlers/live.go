package handlers

import (
	"bufio"
	"fmt"
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/live"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

type LiveHandler struct {
	rdb *redis.Client
}

func NewLive(rdb *redis.Client) *LiveHandler {
	return &LiveHandler{rdb: rdb}
}

// GET /v1/dashboard/live
// Server-Sent Events com eventos em tempo real do projeto: novas sessões,
// leads e conversões. O dashboard mantém a conexão aberta e atualiza os
// contadores sem polling.
//
// Cada mensagem: data: {"type":"session"|"lead"|"conversion","value":0,"label":""}
func (h *LiveHandler) Stream(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	channel := live.Channel(project.ID)

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	heartbeat := time.NewTicker(30 * time.Second)

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer heartbeat.Stop()

		pubsub := h.rdb.Subscribe(c.Context(), channel)
		defer pubsub.Close()
		redisCh := pubsub.Channel()

		fmt.Fprintf(w, "data: {\"connected\":true}\n\n")
		_ = w.Flush()

		for {
			select {
			case msg, ok := <-redisCh:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
				if err := w.Flush(); err != nil {
					return
				}
			case <-heartbeat.C:
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
