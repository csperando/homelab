#!/bin/bash
# Keep the WireGuard endpoint DNS record pointed at the server's current public IPv4.
#
# WG_HOST must resolve straight to the home connection and stay UNPROXIED — Cloudflare's
# proxy only carries HTTP(S), never WireGuard's UDP. (The wg-easy admin UI hostname is a
# separate, proxied record owned by the cloudflared tunnel — do not point this at that.)
#
# One check per invocation. The wg-ddns companion container (services/wireguard/
# compose.yml) runs it on a loop; it also works from cron / a systemd timer.
#
# Cloudflare is touched only when the *locally detected* IP differs from the last one we
# wrote to $DDNS_STATE_FILE, or once every $DDNS_RECONCILE_SECONDS as a drift check.
# Steady state is a single HTTPS GET to an IP-echo service and nothing else.
#
#   CLOUDFLARE_TOKEN          required   API token, Zone:DNS:Edit on the zone
#   CF_RECORD_NAME            required   FQDN to manage (compose passes WG_HOST)
#   CF_ZONE_NAME              optional   defaults to CF_RECORD_NAME minus its first label
#   DDNS_STATE_FILE           optional   default /state/last-ip  (persist across restarts)
#   DDNS_RECONCILE_SECONDS    optional   default 21600 (6h) — force a Cloudflare check
#   DDNS_TTL                  optional   default 60

set -euo pipefail

CF_API_TOKEN="${CLOUDFLARE_TOKEN:?CLOUDFLARE_TOKEN is not set}"
RECORD_NAME="${CF_RECORD_NAME:?CF_RECORD_NAME is not set}"
ZONE_NAME="${CF_ZONE_NAME:-${RECORD_NAME#*.}}"
STATE_FILE="${DDNS_STATE_FILE:-/state/last-ip}"
RECONCILE_SECONDS="${DDNS_RECONCILE_SECONDS:-21600}"
TTL="${DDNS_TTL:-60}"
API="https://api.cloudflare.com/client/v4"

log() { echo "ddns: $*"; }

# --- current public IP (no Cloudflare API, no DNS dependency on the record itself) ------
get_ip() {
  local url ip
  for url in \
    https://1.1.1.1/cdn-cgi/trace \
    https://cloudflare.com/cdn-cgi/trace \
    https://api.ipify.org \
    https://checkip.amazonaws.com
  do
    ip=$(curl -fsS --max-time 10 "$url" 2>/dev/null || true)
    case "$url" in *cdn-cgi/trace) ip=$(printf '%s\n' "$ip" | sed -n 's/^ip=//p');; esac
    ip=$(printf '%s' "$ip" | tr -d '[:space:]')
    [[ $ip =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] && { printf '%s\n' "$ip"; return 0; }
  done
  return 1
}

ip=$(get_ip) || { log "could not determine public IP"; exit 1; }

# --- fast path: nothing changed locally and we reconciled recently --------------------
now=$(date +%s)
if [[ -f $STATE_FILE ]] && read -r last_ip last_check _ < "$STATE_FILE" 2>/dev/null; then
  if [[ $ip == "$last_ip" && ${last_check:-0} =~ ^[0-9]+$ && $(( now - last_check )) -lt $RECONCILE_SECONDS ]]; then
    exit 0
  fi
fi

# --- slow path: ask Cloudflare what the record says, update if needed ------------------
auth=(-H "Authorization: Bearer $CF_API_TOKEN" -H "Content-Type: application/json")
zone_id=$(curl -fsS "${auth[@]}" "$API/zones?name=$ZONE_NAME" | jq -er '.result[0].id')
record=$(curl -fsS "${auth[@]}" "$API/zones/$zone_id/dns_records?type=A&name=$RECORD_NAME")
record_id=$(printf '%s' "$record" | jq -r '.result[0].id // empty')
cf_ip=$(printf '%s' "$record" | jq -r '.result[0].content // empty')

body=$(jq -nc --arg ip "$ip" --arg name "$RECORD_NAME" --argjson ttl "$TTL" \
  '{type:"A", name:$name, content:$ip, ttl:$ttl, proxied:false}')

if [[ -z $record_id ]]; then
  log "$RECORD_NAME missing — creating A -> $ip"
  curl -fsS -X POST "${auth[@]}" "$API/zones/$zone_id/dns_records" --data "$body" >/dev/null
elif [[ $cf_ip != "$ip" ]]; then
  log "$RECORD_NAME $cf_ip -> $ip"
  curl -fsS -X PATCH "${auth[@]}" "$API/zones/$zone_id/dns_records/$record_id" --data "$body" >/dev/null
else
  log "$RECORD_NAME already $ip"
fi

mkdir -p "$(dirname "$STATE_FILE")"
printf '%s %s\n' "$ip" "$now" > "$STATE_FILE"
