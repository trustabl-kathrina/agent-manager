#!/usr/bin/env bash
# start.sh — one-command entry point for the Agent Manager quick-start dev container.
#
# Downloaded/attached as its own release asset (like deployments/vm/bootstrap.sh),
# so it can be fetched and run directly on the host:
#   curl -fsSL <URL>/start.sh -o start.sh && chmod +x start.sh && ./start.sh
#
# It only checks host prerequisites and launches the dev container; install.sh
# is not run automatically — run it yourself from the container shell once it
# starts.
set -euo pipefail

# Stamped to the release version at build time (see .github/scripts/update-install-helpers.sh
# for the equivalent 1.0.0-rc1 -> version substitution pattern applied to this file).
DEFAULT_VERSION="1.0.0-rc1"
IMAGE="${QUICK_START_IMAGE:-ghcr.io/wso2/amp-quick-start}"

log() { printf '\033[0;34m[start]\033[0m %s\n' "$*"; }
warn() { printf '\033[0;33m[start] WARNING:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[0;31m[start] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage: ./start.sh [--version vX.Y.Z]

Checks local prerequisites, then launches the Agent Manager quick-start dev
container. Run ./install.sh from the container shell to install the platform.

  --version   Quick-start image tag to run (default: the version this script
              shipped with, or $QUICK_START_VERSION if set)
EOF
}

VERSION="${QUICK_START_VERSION:-$DEFAULT_VERSION}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:?--version requires a value}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done
VERSION="${VERSION#v}"

log "Checking prerequisites..."

command -v docker >/dev/null 2>&1 || \
  die "Docker is required but was not found on PATH. Install Docker before continuing: https://docs.docker.com/get-docker/"

if ! docker info >/dev/null 2>&1; then
  msg="Docker is installed but the daemon is not reachable (docker info failed)."
  if [[ "$(uname -s)" == "Darwin" ]]; then
    msg+=$'\n'"On macOS, start Colima with a dedicated profile:"
    msg+=$'\n'"  colima start --profile agent-manager --vm-type=vz --vz-rosetta --network-address --cpu 4 --memory 8"
  else
    msg+=$'\n'"Make sure the Docker daemon is running (e.g. 'sudo systemctl start docker')."
  fi
  die "$msg"
fi
log "Docker is installed and reachable"

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64|arm64|aarch64) log "Detected architecture: ${ARCH}" ;;
  *) warn "Unrecognized architecture '${ARCH}' — the quick-start image is published for amd64/arm64 only" ;;
esac

log "Starting quick-start dev container (${IMAGE}:v${VERSION})"
log "Run ./install.sh from the container shell to install the platform (~15-20 minutes)."
exec docker run --rm -it --name amp-quick-start \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --network=host \
  "${IMAGE}:v${VERSION}"
