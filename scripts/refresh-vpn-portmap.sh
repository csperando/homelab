#!/usr/bin/env bash
# Re-request the UPnP port mapping for the WireGuard endpoint. See docs/vpn.md
# "Known issues" — the RAX10's static UDP port-forward rule has repeatedly stopped
# passing packets with no config change on either end (confirmed with an external
# UDP probe + tcpdump showing zero inbound packets); a UPnP-requested mapping has
# gotten it working again every time it's been tried, without touching DMZ or any
# router admin setting. Re-run this any time that happens again.
#
#   INTERNAL_HOST=192.168.1.x ./scripts/refresh-vpn-portmap.sh
#
# INTERNAL_HOST is the server's LAN IP — required, no default (kept out of the repo
# per the "no real LAN details in tracked files" convention). EXTERNAL_PORT/
# INTERNAL_PORT default to the WireGuard default (51820); override if WG_UDP_PORT
# differs on the server. Safe to re-run — re-requesting an existing mapping just
# refreshes it.
set -euo pipefail

INTERNAL_HOST="${INTERNAL_HOST:?Set INTERNAL_HOST to the server's LAN IP, e.g. INTERNAL_HOST=192.168.1.x $0}"
EXTERNAL_PORT="${EXTERNAL_PORT:-51820}"
INTERNAL_PORT="${INTERNAL_PORT:-51820}"
PROTOCOL="${PROTOCOL:-UDP}"
DESCRIPTION="${DESCRIPTION:-wireguard}"
LEASE_DURATION="${LEASE_DURATION:-0}" # 0 = request unlimited; router may reject and need a fixed value

if ! command -v upnpc >/dev/null; then
  echo "==> Installing miniupnpc"
  sudo apt-get update -qq
  sudo apt-get install -y -qq miniupnpc
fi

echo "==> Requesting UPnP mapping: ${PROTOCOL} ${EXTERNAL_PORT} -> ${INTERNAL_HOST}:${INTERNAL_PORT}"
upnpc -e "$DESCRIPTION" -a "$INTERNAL_HOST" "$INTERNAL_PORT" "$EXTERNAL_PORT" "$PROTOCOL" "$LEASE_DURATION"

echo
echo "==> Current mappings on the router"
upnpc -l
