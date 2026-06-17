#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# deploy.sh — sobe front (Next.js) + back (Go API) + infra no servidor remoto.
#
# Fluxo:
#   1. valida build local (go build + next build)        [pula com SKIP_CHECKS=true]
#   2. rsync do código (convtrack/ e convtrack-web/) via SSH
#   3. docker compose build + up -d NO servidor
#   4. health check
#
# Pré-requisitos no SEU computador: ssh, rsync, go, npm, e a chave SSH já
# carregada (ssh-agent) — este script NÃO digita senha nem manipula chaves.
#
# Pré-requisitos no SERVIDOR: docker + docker compose, e um arquivo
#   ${REMOTE_DIR}/convtrack/.env  com as variáveis de produção (JWT_SECRET,
#   ENCRYPTION_KEY, SMTP_*, etc.). O script NÃO envia o .env (segredos ficam
#   só no servidor) e o rsync NÃO o apaga.
#
# Uso:  cp deploy.env.example deploy.env && nano deploy.env && ./deploy.sh
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

cd "$(dirname "$0")"

# ── Carrega config ───────────────────────────────────────────────────────────
if [ -f deploy.env ]; then
  set -a; # shellcheck disable=SC1091
  source deploy.env; set +a
else
  echo "✗ deploy.env não encontrado. Rode: cp deploy.env.example deploy.env"
  exit 1
fi

: "${SSH_HOST:?defina SSH_HOST no deploy.env}"
: "${SSH_USER:?defina SSH_USER no deploy.env}"
SSH_PORT="${SSH_PORT:-22}"
REMOTE_DIR="${REMOTE_DIR:-/opt/convtrack}"
SKIP_CHECKS="${SKIP_CHECKS:-false}"

SSH_BASE=(ssh -p "${SSH_PORT}" "${SSH_USER}@${SSH_HOST}")
SRC_BACK="$(pwd)"
SRC_FRONT="$(pwd)/../convtrack-web"

if [ ! -d "$SRC_FRONT" ]; then
  echo "✗ Front não encontrado em $SRC_FRONT (convtrack/ e convtrack-web/ devem ser irmãos)"
  exit 1
fi

echo "▶ Destino: ${SSH_USER}@${SSH_HOST}:${SSH_PORT}  →  ${REMOTE_DIR}"

# ── 1. Validação local ───────────────────────────────────────────────────────
if [ "$SKIP_CHECKS" != "true" ]; then
  echo "▶ 1/4 Validando build local…"
  ( cd "$SRC_BACK"  && go build ./... )            || { echo "✗ go build falhou"; exit 1; }
  ( cd "$SRC_FRONT" && npm run build >/dev/null )  || { echo "✗ next build falhou"; exit 1; }
  echo "  ✓ builds locais OK"
else
  echo "▶ 1/4 Validação local pulada (SKIP_CHECKS=true)"
fi

# ── 2. Sincroniza código ─────────────────────────────────────────────────────
echo "▶ 2/4 Garantindo diretórios e sincronizando código…"
"${SSH_BASE[@]}" "mkdir -p '${REMOTE_DIR}/convtrack' '${REMOTE_DIR}/convtrack-web'"

RSYNC=(rsync -az --delete --human-readable -e "ssh -p ${SSH_PORT}")

# Backend (preserva o .env de produção do servidor)
"${RSYNC[@]}" \
  --exclude '.git' --exclude 'bin' --exclude '*.log' \
  --exclude '.env' --exclude 'deploy.env' --exclude 'deploy.env.example' \
  "$SRC_BACK/" "${SSH_USER}@${SSH_HOST}:${REMOTE_DIR}/convtrack/"

# Frontend (sem node_modules/.next — são reconstruídos no build)
"${RSYNC[@]}" \
  --exclude '.git' --exclude 'node_modules' --exclude '.next' --exclude '*.log' \
  "$SRC_FRONT/" "${SSH_USER}@${SSH_HOST}:${REMOTE_DIR}/convtrack-web/"
echo "  ✓ código sincronizado"

# ── 3. Build + up no servidor ────────────────────────────────────────────────
echo "▶ 3/4 Build + up (docker compose) no servidor…"
"${SSH_BASE[@]}" "set -e
  cd '${REMOTE_DIR}/convtrack'
  if [ ! -f .env ]; then
    echo '⚠  Atenção: ${REMOTE_DIR}/convtrack/.env não existe no servidor — usando defaults do compose (NÃO recomendado em produção).'
  fi
  docker compose build
  docker compose up -d --remove-orphans
"

# ── 4. Health check ──────────────────────────────────────────────────────────
echo "▶ 4/4 Status dos containers:"
"${SSH_BASE[@]}" "cd '${REMOTE_DIR}/convtrack' && docker compose ps"

echo ""
echo "✓ Deploy concluído em ${SSH_HOST}."
echo "  Dica: 'ssh -p ${SSH_PORT} ${SSH_USER}@${SSH_HOST} \"cd ${REMOTE_DIR}/convtrack && docker compose logs -f api\"' para acompanhar os logs."
