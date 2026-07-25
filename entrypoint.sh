#!/bin/bash
set -e

# Seed tracked Claude defaults (settings, skills) into the persistent,
# bind-mounted /root/.claude on first boot without clobbering runtime state
# (credentials, sessions, etc.) that may already live there.
cp -rn /opt/claude-defaults/. /root/.claude/ 2>/dev/null || true
[ -s /root/.claude/claude.json ] || echo '{}' > /root/.claude/claude.json
ln -sf /root/.claude/claude.json /root/.claude.json

/usr/local/bin/homelab-healthcheck &

exec "$@"
