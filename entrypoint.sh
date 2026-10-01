#!/bin/bash
set -e

# Sync the image's Claude config (/opt/bit-brain, from the repo's bit-brain/)
# into the persistent, bind-mounted /root/.claude.
#
# Image-owned paths: always replaced wholesale, so edits and deletions in
# bit-brain/ reach an existing volume. Anything created at these paths at
# runtime is discarded on restart.
image_owned="CLAUDE.md skills hooks agents commands output-styles"
# Runtime-owned: seeded on first boot only, never overwritten, since Claude
# Code writes to these itself (permissions, /model, /config, ...).
seed_once="settings.json settings.local.json"

mkdir -p /root/.claude
for name in $image_owned; do
  rm -rf "/root/.claude/$name"
  if [ -e "/opt/bit-brain/$name" ]; then
    cp -a "/opt/bit-brain/$name" "/root/.claude/$name"
  fi
done
for name in $seed_once; do
  if [ -e "/opt/bit-brain/$name" ]; then
    cp -n "/opt/bit-brain/$name" "/root/.claude/$name"
  fi
done
# Never copy anything else — it could clobber runtime state (credentials,
# sessions, projects/memory, ...).
for path in /opt/bit-brain/* /opt/bit-brain/.[!.]*; do
  [ -e "$path" ] || continue
  name="$(basename "$path")"
  case " $image_owned $seed_once " in
    *" $name "*) ;;
    *) echo "entrypoint: ignoring unrecognized bit-brain entry: $name" >&2 ;;
  esac
done

[ -s /root/.claude/claude.json ] || echo '{}' > /root/.claude/claude.json
ln -sf /root/.claude/claude.json /root/.claude.json

/usr/local/bin/homelab-healthcheck &

exec "$@"
