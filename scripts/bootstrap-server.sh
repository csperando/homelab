#!/usr/bin/env bash
# One-time (re-runnable) setup for the homelab dev server. See docs/deploy.md.
#
#   RUNNER_TOKEN=<token> ./scripts/bootstrap-server.sh
#
# Get RUNNER_TOKEN from the repo: Settings > Actions > Runners > New self-hosted
# runner (it is shown in the "Configure" section, valid ~1 hour).
#
# Run as your normal login user, not root - the script calls sudo only where it
# needs to (packages, docker group, enabling docker on boot). Every step is a
# no-op if already done, so it is safe to re-run. Deploy dir defaults to
# ~/homelab; after the first deploy a copy of this script lives there too.
set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/csperando/homelab}"
DEPLOY_DIR="${DEPLOY_DIR:-$HOME/homelab}"
RUNNER_DIR="${RUNNER_DIR:-$HOME/actions-runner}"
RUNNER_VERSION="${RUNNER_VERSION:-2.337.0}"
RUNNER_SHA256="${RUNNER_SHA256:-}" # optional: verify the runner tarball
TARGET_USER="${USER:-$(id -un)}"

if [ "$(id -u)" -eq 0 ]; then
  echo "Run this as your normal login user, not root (it uses sudo where needed)." >&2
  exit 1
fi
if ! command -v apt-get >/dev/null; then
  echo "This script expects a Debian/Ubuntu host (apt-get not found)." >&2
  exit 1
fi

echo "==> Installing packages (rsync, make, curl, tar)"
sudo apt-get update -qq
sudo apt-get install -y -qq rsync make curl tar

echo "==> Ensuring '$TARGET_USER' is in the 'docker' group"
if id -nG "$TARGET_USER" | tr ' ' '\n' | grep -qx docker; then
  echo "    already a member"
  GROUP_ADDED=0
else
  sudo usermod -aG docker "$TARGET_USER"
  echo "    added"
  GROUP_ADDED=1
fi

echo "==> Enabling docker.service (start on boot)"
sudo systemctl enable --now docker

echo "==> Creating deploy dir $DEPLOY_DIR"
mkdir -p "$DEPLOY_DIR"

if [ -z "${RUNNER_TOKEN:-}" ]; then
  echo
  echo "RUNNER_TOKEN not set — skipping the GitHub Actions runner install."
  echo "Grab a token from ${REPO_URL}/settings/actions/runners/new and re-run:"
  echo "  RUNNER_TOKEN=<token> $0"
  exit 0
fi

echo "==> Installing GitHub Actions runner v$RUNNER_VERSION into $RUNNER_DIR"
mkdir -p "$RUNNER_DIR"
cd "$RUNNER_DIR"
tarball="actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
if [ ! -x ./run.sh ]; then
  curl -fsSL -o "$tarball" \
    "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/${tarball}"
  if [ -n "$RUNNER_SHA256" ]; then
    echo "${RUNNER_SHA256}  ${tarball}" | sha256sum -c -
  fi
  tar xzf "$tarball"
  rm -f "$tarball"
fi

echo "==> Registering runner with $REPO_URL"
./config.sh \
  --url "$REPO_URL" \
  --token "$RUNNER_TOKEN" \
  --unattended \
  --replace \
  --name "$(hostname)" \
  --labels self-hosted,linux,X64

echo "==> Installing + starting the runner service"
if [ -f .service ]; then
  echo "    service already installed"
  sudo ./svc.sh start || true
else
  sudo ./svc.sh install "$TARGET_USER"
  sudo ./svc.sh start
fi

echo
echo "Done. Remaining steps:"
if [ "$GROUP_ADDED" -eq 1 ]; then
  echo "  - Log fully out and back in so the 'docker' group membership takes effect."
fi
echo "  - Optional: 'docker login ghcr.io' with a read:packages PAT for manual pulls/rollback."
echo "  - Trigger the first deploy from ${REPO_URL}/actions/workflows/deploy.yml (Run workflow)."
