#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# deploy-prod.sh — sobe no servidor usando as imagens já publicadas no Docker Hub.
#
#  Envia só 3 arquivos (compose + Caddyfile + .env) e roda pull + up -d.
#  NÃO builda nada no servidor.
#
#  Pré-requisitos:
#    - ./build-push.sh já rodado (imagens no Docker Hub).
#    - deploy.env preenchido (SSH_HOST, SSH_USER, SSH_PORT, REMOTE_DIR).
#    - chave SSH no ssh-agent (sem senha — política).
#    - servidor com docker + docker compose.
#
#  Uso:  ./deploy-prod.sh
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail
cd "$(dirname "$0")"

[ -f deploy.env ] || { echo "✗ deploy.env não encontrado (cp deploy.env.example deploy.env)"; exit 1; }
set -a; # shellcheck disable=SC1091
source deploy.env; set +a

: "${SSH_HOST:?defina SSH_HOST}" "${SSH_USER:?defina SSH_USER}"
SSH_PORT="${SSH_PORT:-22}"
REMOTE_DIR="${REMOTE_DIR:-/opt/convtrack}"
SSH=(ssh -p "$SSH_PORT" "${SSH_USER}@${SSH_HOST}")
SCP=(scp -P "$SSH_PORT")

echo "▶ 1/4 Preparando diretório no servidor…"
"${SSH[@]}" "mkdir -p '${REMOTE_DIR}'"

echo "▶ 2/4 Enviando compose + Caddyfile…"
"${SCP[@]}" docker-compose.prod.yml Caddyfile.prod "${SSH_USER}@${SSH_HOST}:${REMOTE_DIR}/"

echo "▶ 2b/4 Enviando .env (só se ainda não existir no servidor)…"
if "${SSH[@]}" "test -f '${REMOTE_DIR}/.env'"; then
  echo "    .env já existe no servidor — mantido (edite lá se precisar)."
else
  "${SCP[@]}" .env.production "${SSH_USER}@${SSH_HOST}:${REMOTE_DIR}/.env"
  echo "    .env enviado."
fi

echo "▶ 3/4 Pull das imagens + up -d…"
"${SSH[@]}" "set -e
  cd '${REMOTE_DIR}'
  docker compose -f docker-compose.prod.yml --env-file .env pull
  docker compose -f docker-compose.prod.yml --env-file .env up -d --remove-orphans
"

echo "▶ 4/4 Status:"
"${SSH[@]}" "cd '${REMOTE_DIR}' && docker compose -f docker-compose.prod.yml ps"

echo ""
echo "✓ Deploy concluído."
echo "  Dashboard: https://cloakhide.com.br"
echo "  API:       https://cloaker.cloakhide.com.br/health"
echo "  Logs:      ssh -p ${SSH_PORT} ${SSH_USER}@${SSH_HOST} \"cd ${REMOTE_DIR} && docker compose -f docker-compose.prod.yml logs -f api\""
