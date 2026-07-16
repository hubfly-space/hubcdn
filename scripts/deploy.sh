#!/usr/bin/env bash
# Deploys hubCDN to a remote server that already has Docker (with the
# Compose plugin) installed, over SSH + rsync. Invoked via `make deploy`.
#
# Verifies prerequisites, installs rsync on the remote if it's missing,
# ships the source tree, ensures a .env exists on the remote with hubCDN's
# standard production values (without clobbering one you've customized
# there), and runs `docker compose up -d --build`.
set -euo pipefail

DEPLOY_HOST="${DEPLOY_HOST:-dev@192.168.1.3}"
# Relative to the remote user's home directory. Not "~/hubcdn": a literal
# "~" here would be tilde-expanded by the *local* shell (using the local
# user's $HOME) before ssh ever sees it. A bare relative path avoids that
# entirely — every ssh/rsync invocation below starts in the remote user's
# own home directory by default.
DEPLOY_DIR="${DEPLOY_DIR:-hubcdn}"

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
HUBCDN_HTTPS_PORT="${HUBCDN_HTTPS_PORT:-4403}"

# Every ssh/rsync call below shares one authenticated connection, so a
# password (or a sudo prompt, if rsync needs installing) is only asked for
# once per deploy instead of once per connection.
SSH_CTL_DIR="$(mktemp -d)"
SSH_CTL="${SSH_CTL_DIR}/ctl.sock"
SSH_CMD="ssh -o ControlMaster=auto -o ControlPath=${SSH_CTL} -o ControlPersist=120"
cleanup() {
	$SSH_CMD -O exit "${DEPLOY_HOST}" >/dev/null 2>&1 || true
	rm -rf "${SSH_CTL_DIR}"
}
trap cleanup EXIT

ssh_remote() { $SSH_CMD "${DEPLOY_HOST}" "$@"; }

echo "==> Deploying hubCDN to ${DEPLOY_HOST}:${DEPLOY_DIR}"

echo "==> Checking prerequisites on ${DEPLOY_HOST}"
if ! ssh_remote 'command -v docker >/dev/null 2>&1'; then
	echo "error: docker is not installed on ${DEPLOY_HOST}" >&2
	exit 1
fi
if ! ssh_remote 'docker compose version >/dev/null 2>&1'; then
	echo "error: the Docker Compose plugin is not available on ${DEPLOY_HOST} (docker compose version failed)" >&2
	exit 1
fi
if ! ssh_remote 'command -v rsync >/dev/null 2>&1'; then
	echo "==> rsync not found on ${DEPLOY_HOST}, installing it"
	# -t: sudo needs a real terminal for its password prompt, which lands
	# in your terminal since you're the one running `make deploy`.
	$SSH_CMD -t "${DEPLOY_HOST}" '
		set -e
		if command -v apt-get >/dev/null 2>&1; then
			sudo apt-get update -qq && sudo apt-get install -y -qq rsync
		elif command -v dnf >/dev/null 2>&1; then
			sudo dnf install -y -q rsync
		elif command -v yum >/dev/null 2>&1; then
			sudo yum install -y -q rsync
		elif command -v apk >/dev/null 2>&1; then
			sudo apk add --no-cache rsync
		elif command -v pacman >/dev/null 2>&1; then
			sudo pacman -Sy --noconfirm rsync
		else
			echo "no supported package manager found; install rsync manually" >&2
			exit 1
		fi
	'
	if ! ssh_remote 'command -v rsync >/dev/null 2>&1'; then
		echo "error: rsync install on ${DEPLOY_HOST} did not succeed" >&2
		exit 1
	fi
	echo "==> rsync installed"
fi

ssh_remote "mkdir -p '${DEPLOY_DIR}'"

echo "==> Syncing source tree"
# Leading slashes anchor these to the repo root — unanchored patterns like
# 'hubcdn' would also match cmd/hubcdn/ (the source directory) and strip it
# from the tree entirely.
rsync -az --delete \
	-e "$SSH_CMD" \
	--exclude '/.git' \
	--exclude '/data' \
	--exclude '/.env' \
	--exclude '*.log' \
	--exclude '/hubcdn' \
	./ "${DEPLOY_HOST}:${DEPLOY_DIR}/"

echo "==> Ensuring remote .env and starting the stack"
# shellcheck disable=SC2087
$SSH_CMD "${DEPLOY_HOST}" bash -s -- \
	"${DEPLOY_DIR}" "${HUBCDN_ACME_EMAIL}" "${HUBCDN_PUBLIC_IPS}" "${HUBCDN_HOSTNAME}" \
	"${HUBCDN_ACME_STAGING}" "${HUBCDN_HTTPS_PORT}" <<'REMOTE'
set -euo pipefail
DIR="$1"; EMAIL="$2"; IPS="$3"; HOSTNAME_="$4"; STAGING="$5"; HTTPS_PORT="$6"
cd "$DIR"

if [ ! -f .env ]; then
	cat > .env <<EOF
HUBCDN_ACME_EMAIL=${EMAIL}
HUBCDN_PUBLIC_IPS=${IPS}
HUBCDN_HOSTNAME=${HOSTNAME_}
HUBCDN_ACME_STAGING=${STAGING}
HUBCDN_HTTPS_PORT=${HTTPS_PORT}
HUBCDN_HOST_HTTPS_PORT=${HTTPS_PORT}
EOF
	echo "created .env with default hubCDN production values"
else
	echo ".env already present on remote, leaving it untouched"
fi

# Read the port actually in effect (the existing .env wins over the
# freshly computed default above), and fail before wasting a build if
# something else on this host already holds it.
port_from_env() {
	awk -F= -v k="$1" -v d="$2" '$1==k{v=$2} END{print (v=="" ? d : v)}' .env
}
effective_https=$(port_from_env HUBCDN_HOST_HTTPS_PORT "$HTTPS_PORT")

if command -v ss >/dev/null 2>&1; then
	if ss -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[.:]${effective_https}\$"; then
		echo "error: port ${effective_https} is already in use on this host by another process." >&2
		echo "  see what's using it (on this host, sudo shows the process): ss -ltnp | grep :${effective_https}" >&2
		echo "  or edit HUBCDN_HOST_HTTPS_PORT in ${DIR}/.env and rerun." >&2
		exit 1
	fi
fi

docker compose build
docker compose up -d
docker compose ps
REMOTE

echo "==> Deployed. Follow logs with: make remote-logs"
