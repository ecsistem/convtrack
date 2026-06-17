#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# build-push.sh — builda as imagens (API + WEB) e publica no Docker Hub.
#
#  IMPORTANTE: builda para linux/amd64 (servidor x86) mesmo no Mac ARM, via buildx.
#
#  Pré-requisitos: docker + buildx, e estar logado:  docker login
#  (o `docker login` é VOCÊ que roda — a senha/token não passa por aqui.)
#
#  Uso:  ./build-push.sh            # tag do .env.production (default latest)
#        IMAGE_TAG=v1 ./build-push.sh
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail
cd "$(dirname "$0")"

if [ -f .env.production ]; then
  set -a; # shellcheck disable=SC1091
  source .env.production; set +a
fi

: "${DOCKERHUB_USER:?defina DOCKERHUB_USER no .env.production}"
TAG="${IMAGE_TAG:-latest}"
APIURL="${NEXT_PUBLIC_API_URL:?defina NEXT_PUBLIC_API_URL no .env.production}"
PLATFORM="${BUILD_PLATFORM:-linux/amd64}"
FRONT_DIR="$(pwd)/../convtrack-web"

if [ "$DOCKERHUB_USER" = "SEU_USUARIO_DOCKERHUB" ]; then
  echo "✗ Edite DOCKERHUB_USER no .env.production com seu usuário do Docker Hub."
  exit 1
fi
[ -d "$FRONT_DIR" ] || { echo "✗ Front não encontrado em $FRONT_DIR"; exit 1; }

echo "▶ Usuário Docker Hub: ${DOCKERHUB_USER} | tag: ${TAG} | plataforma: ${PLATFORM}"

# Builder buildx dedicado (cria se não existir)
docker buildx inspect ctbuilder >/dev/null 2>&1 || docker buildx create --name ctbuilder >/dev/null
docker buildx use ctbuilder

echo "▶ 1/2 API → ${DOCKERHUB_USER}/convtrack-api:${TAG}"
docker buildx build --platform "$PLATFORM" \
  -t "${DOCKERHUB_USER}/convtrack-api:${TAG}" \
  -f Dockerfile . --push

echo "▶ 2/2 WEB → ${DOCKERHUB_USER}/convtrack-web:${TAG}  (NEXT_PUBLIC_API_URL=${APIURL})"
docker buildx build --platform "$PLATFORM" \
  --build-arg NEXT_PUBLIC_API_URL="$APIURL" \
  -t "${DOCKERHUB_USER}/convtrack-web:${TAG}" \
  "$FRONT_DIR" --push

echo ""
echo "✓ Publicado no Docker Hub:"
echo "    ${DOCKERHUB_USER}/convtrack-api:${TAG}"
echo "    ${DOCKERHUB_USER}/convtrack-web:${TAG}"
echo "  Agora rode ./deploy-prod.sh para subir no servidor."
