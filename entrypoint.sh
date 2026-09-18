#!/bin/bash
set -e

# Seed Claude defaults (settings, skills) into the persistent, bind-mounted
# /root/.claude on first boot without clobbering runtime state (credentials,
# sessions, etc.) that may already live there.
cp -rn /opt/claude-defaults/. /root/.claude/ 2>/dev/null || true
[ -s /root/.claude/claude.json ] || echo '{}' > /root/.claude/claude.json
ln -sf /root/.claude/claude.json /root/.claude.json

# Skills are image-managed, not runtime state: the no-clobber seed above would
# leave stale skills from an earlier image in place, so a freshly pulled image
# never updated them. Force-refresh just the skills subtree from the image on
# every boot, leaving credentials / sessions / settings untouched.
if [ -d /opt/claude-defaults/skills ]; then
  rm -rf /root/.claude/skills
  cp -r /opt/claude-defaults/skills /root/.claude/skills
fi

/usr/local/bin/homelab-healthcheck &

exec "$@"
