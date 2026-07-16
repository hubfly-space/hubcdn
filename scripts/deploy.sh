#!/usr/bin/env bash
# Deploys hubCDN to a remote server that already has Docker (with the
# Compose plugin) installed, over SSH + rsync. Invoked via `make deploy`.
#
# Ships the source tree, ensures a .env exists on the remote with hubCDN's
# standard production values (without clobbering one you've customized
# there), and runs `docker compose up -d --build`.
set -euo pipefail

DEPLOY_HOST="${DEPLOY_HOST:-dev@192.168.1.3}"
DEPLOY_DIR="${DEPLOY_DIR:-~/hubcdn}"

# Standing defaults for this deployment. Override by exporting the same
# variable before calling `make deploy`, e.g.:
#   HUBCDN_ACME_STAGING=true make deploy
DEFAULT_ACME_EMAIL="ops@hubcdn.space"
DEFAULT_PUBLIC_IPS="41.186.167.39"
DEFAULT_HOSTNAME="cdn.hubcdn.space"

HUBCDN_ACME_EMAIL="${HUBCDN_ACME_EMAIL:-$DEFAULT_ACME_EMAIL}"
HUBCDN_PUBLIC_IPS="${HUBCDN_PUBLIC_IPS:-$DEFAULT_PUBLIC_IPS}"
HUBCDN_HOSTNAME="${HUBCDN_HOSTNAME:-$DEFAULT_HOSTNAME}"
HUBCDN_ACME_STAGING="${HUBCDN_ACME_STAGING:-false}"
HUBCDN_HTTP_PORT="${HUBCDN_HTTP_PORT:-8080}"
HUBCDN_HTTPS_PORT="${HUBCDN_HTTPS_PORT:-4403}"

echo "==> Deploying hubCDN to ${DEPLOY_HOST}:${DEPLOY_DIR}"

ssh "${DEPLOY_HOST}" "mkdir -p '${DEPLOY_DIR}'"

echo "==> Syncing source tree"
rsync -az --delete \
	--exclude '.git' \
	--exclude 'data' \
	--exclude '.env' \
	--exclude '*.log' \
	--exclude 'hubcdn' \
	./ "${DEPLOY_HOST}:${DEPLOY_DIR}/"

echo "==> Ensuring remote .env and starting the stack"
# shellcheck disable=SC2087
ssh "${DEPLOY_HOST}" bash -s -- \
	"${DEPLOY_DIR}" "${HUBCDN_ACME_EMAIL}" "${HUBCDN_PUBLIC_IPS}" "${HUBCDN_HOSTNAME}" \
	"${HUBCDN_ACME_STAGING}" "${HUBCDN_HTTP_PORT}" "${HUBCDN_HTTPS_PORT}" <<'REMOTE'
set -euo pipefail
DIR="$1"; EMAIL="$2"; IPS="$3"; HOSTNAME_="$4"; STAGING="$5"; HTTP_PORT="$6"; HTTPS_PORT="$7"
cd "$DIR"

if [ ! -f .env ]; then
	cat > .env <<EOF
HUBCDN_ACME_EMAIL=${EMAIL}
HUBCDN_PUBLIC_IPS=${IPS}
HUBCDN_HOSTNAME=${HOSTNAME_}
HUBCDN_ACME_STAGING=${STAGING}
HUBCDN_HTTP_PORT=${HTTP_PORT}
HUBCDN_HTTPS_PORT=${HTTPS_PORT}
HUBCDN_HOST_HTTP_PORT=${HTTP_PORT}
HUBCDN_HOST_HTTPS_PORT=${HTTPS_PORT}
EOF
	echo "created .env with default hubCDN production values"
else
	echo ".env already present on remote, leaving it untouched"
fi

docker compose build
docker compose up -d
docker compose ps
REMOTE

echo "==> Deployed. Follow logs with: make remote-logs"
