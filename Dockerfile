# ── Build stage ───────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Dependências primeiro (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Código-fonte completo (inclui migrations/ que são embedded via go:embed)
COPY . .

# Build estático — sem CGO, sem libs externas
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o convtrack \
    ./cmd/server

# ── Runtime stage ─────────────────────────────────────────────────────────
FROM alpine:3.20

# TLS certs + timezone + ffmpeg (camuflagem de vídeo frame a frame)
RUN apk --no-cache add ca-certificates tzdata ffmpeg

WORKDIR /app

# Só o binário (migrations já estão embutidas)
COPY --from=builder /app/convtrack .

# Assets públicos (tracker.js, shield-fp.js, rrweb)
COPY --from=builder /app/public ./public

EXPOSE 8080

# Healthcheck interno
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./convtrack"]
