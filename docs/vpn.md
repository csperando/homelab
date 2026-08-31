# VPN runbook (wg-easy + cloudflared)

The `wireguard` (wg-easy v14) and `cloudflared` infra services provide a WireGuard VPN.
Both are gated behind the `vpn` compose profile, so they run **only on the server**, which
activates them with `COMPOSE_PROFILES=vpn` in its `.env` (via the `ENV` secret).

**Model**

- **WireGuard UDP endpoint** — the only inbound port. Published on `WG_UDP_PORT`
  (default 51820) and forwarded on the router. `WG_HOST` is a **DNS-only (unproxied)**
  record on the home public IP — the handshake is UDP and Cloudflare's proxy only carries
  HTTP(S), so an orange-clouded record black-holes it. This is a *separate* record from
  the admin-UI hostname below. The `wg-ddns` companion service keeps its A value in sync
  with the (dynamic) public IP.
- **wg-easy admin UI** — no inbound port. `cloudflared` dials out to Cloudflare; a
  Cloudflare Tunnel routes a public hostname (proxied) to `http://wireguard:${WG_UI_PORT}`,
  with **Cloudflare Access** enforcing login at the edge. wg-easy keeps its own login
  behind Access.
- **Full tunnel** — clients get `WG_ALLOWED_IPS=0.0.0.0/0` (IPv4 only; the container has
  no IPv6 egress, so advertising `::/0` would black-hole clients' v6 traffic). All their
  traffic egresses through the server. `WG_POST_UP` in `services/wireguard/compose.yml`
  drops forwarded `wg0` traffic to every RFC1918 range and blocks the UI port on `wg0`, so
  a client (or a stolen client key) is a bare internet exit — no route to the container
  network, the home LAN, or the admin UI.

> Keep real hostnames, public IPs, LAN CIDRs, the tunnel token, the API token, and the
> password hash out of every tracked file. They live only in the `ENV` secret / the
> server's `.env`. Placeholders below: `<WG_HOSTNAME>` (endpoint, DNS-only),
> `<UI_HOSTNAME>` (admin UI, proxied), `<UDP_PORT>`.

---

## One-time setup

### 1. Cloudflare (dashboard)

1. **Zero Trust** — enable it on the account (Cloudflare dashboard → Zero Trust), pick a
   team name, choose the free plan.
2. **Create the tunnel** — Zero Trust → Networks → Tunnels → *Create a tunnel* →
   *Cloudflared* → name it → **copy the token** (`eyJ...`).
3. **UI public hostname** — on the tunnel, *Public Hostname* → *Add*:
   - Subdomain/domain: `<UI_HOSTNAME>`
   - Service: `HTTP` → `wireguard:51821` (use the `WG_UI_PORT` value)
   - This writes the DNS record automatically (proxied). Remove any old A record for that
     name.
4. **Access application** — Zero Trust → Access → Applications → *Add an application* →
   *Self-hosted*:
   - Application domain: `<UI_HOSTNAME>`
   - Policy: *Allow* → include your email(s); identity method one-time PIN (no IdP setup)
     or an SSO provider.
5. **Endpoint DNS record + API token** — for `<WG_HOSTNAME>` (must differ from
   `<UI_HOSTNAME>`):
   - DNS → *Add record*: `A`, name `<WG_HOSTNAME>`, any placeholder IP, **Proxy status:
     DNS only (grey cloud)**. `wg-ddns` overwrites the IP on first run; it will not
     un-proxy an existing orange-clouded record, so set it grey here.
   - My Profile → *API Tokens* → *Create Token* → *Edit zone DNS* template → Zone
     Resources: this zone → **copy the token**. This is `CLOUDFLARE_TOKEN`.
6. **Router** — forward inbound `UDP <UDP_PORT>` to the server's LAN IP. The forwarded
   external port, the port the host publishes (`WG_UDP_PORT`), and the port in each
   client's `Endpoint` line are **all the same number** — wg-easy derives the client port
   from `WG_UDP_PORT` (compose maps `WG_UDP_PORT:51820/udp`). A mismatch here is the usual
   cause of "handshake did not complete". No other inbound ports.

### 2. Secrets → the `ENV` GitHub secret

Add to the `ENV` secret (repo → Settings → Secrets and variables → Actions → `ENV`):

```
COMPOSE_PROFILES=vpn
WG_HOST=<WG_HOSTNAME>
WG_UDP_PORT=<UDP_PORT>
WG_UI_PORT=51821
WG_DEFAULT_DNS=1.1.1.1
WG_ALLOWED_IPS=0.0.0.0/0
PASSWORD_HASH=<hash from below>
TUNNEL_TOKEN=<token from step 2>
CLOUDFLARE_TOKEN=<token from step 5>
# CF_ZONE_NAME=   # optional; wg-ddns derives it from WG_HOST when blank
```

Without `COMPOSE_PROFILES=vpn` the deploy's `up -d --remove-orphans` will delete the
containers each run.

### 3. Admin password hash

```sh
docker run --rm ghcr.io/wg-easy/wg-easy:14 wgpw 'YOUR_ADMIN_PASSWORD'
# -> PASSWORD_HASH='$2b$12$....'
```

Put the hash (the part inside the quotes) into `PASSWORD_HASH`. If a later
`docker compose --profile vpn config` mangles it or warns about `$`-variables, double
every `$` to `$$` in the value.

### 4. Deploy

Merge to `main` (or *Run workflow* on *deploy*). Then on the server:

```sh
cd ~/homelab
docker compose -f docker-compose.yml -f docker-compose.prod.yml ps      # wireguard + wg-ddns + cloudflared up
docker logs --tail 20 cloudflared                                       # "Registered tunnel connection"
docker logs --tail 5 homelab-wg-ddns-1                                  # "ddns: <WG_HOSTNAME> already <ip>" (or created)
docker exec homelab-wireguard-1 wg show
dig +short <WG_HOSTNAME>                                                # == the server's public IP, and NOT a Cloudflare IP
```

---

## Adding a user

1. Open `https://<UI_HOSTNAME>`, pass Cloudflare Access, then the wg-easy login.
2. *New Client* → name it → download the `.conf` or show the QR.
3. Send it to the user out of band.

**End-user note to include:**

> Install the official **WireGuard** app (not OpenVPN). Import the `.conf` file, or scan
> the QR from the app. While the tunnel is on, *all* your traffic — and your public IP —
> routes through the home connection; turn it off for normal browsing speed.

---

## Dynamic DNS (`wg-ddns`)

`wg-ddns` keeps the `WG_HOST` A record pointed at the home public IP. Clients use the
hostname in their `Endpoint`, so an IP change is transparent once the record updates.

- **Cost:** each `DDNS_INTERVAL` tick (default 60s) is one HTTPS GET to an IP-echo
  service. The Cloudflare API is called **only** when the detected IP differs from
  `volume/infra/wg-ddns/last-ip`, or once per `DDNS_RECONCILE_SECONDS` (default 6h) as a
  drift check. Steady state adds nothing to Cloudflare.
- **State:** `volume/infra/wg-ddns/last-ip` (`<ip> <unix-ts>`). Safe to delete — the next
  run just does one reconcile against Cloudflare and rewrites it.
- **Changeover downtime** after an IP change ≈ `DDNS_INTERVAL` + record `ttl` (60s) +
  however long the client takes to retry and re-resolve (WireGuard apps re-resolve the
  endpoint on handshake failure, ~1–2 min). Lower `DDNS_INTERVAL` to shrink the first
  term; the rest is client-side.
- **Token:** `CLOUDFLARE_TOKEN`, scoped `Zone:DNS:Edit` on the zone only. Check it with
  `docker logs homelab-wg-ddns-1`.

---

## Known issues / follow-ups

- **RAX10 port-forward activation.** The UDP `WG_UDP_PORT` forward did not take effect
  after a plain router reboot — it only started passing packets after toggling the
  Default DMZ Server on and off (which forces a NAT-table reload). It may need that again
  after a firmware update or power loss. The server is on Wi-Fi (`wlp1s0`); if this
  recurs, try a wired connection (some Nighthawk firmware only forwards to wired clients)
  or script a DMZ toggle. Symptom: `tcpdump -ni any udp port <WG_UDP_PORT>` on the server
  shows nothing while a client connects, but a DMZ to the server works.
- **`WG_PERSISTENT_KEEPALIVE`.** Not set (wg-easy default 0). Setting it to `25` in the
  `ENV` secret would keep sleeping phones' NAT mappings warm and make a dead tunnel
  recover faster after an IP change. Regenerate clients after changing it.

---

## Backup

`volume/infra/wireguard/` holds the server keys and every peer. Back it up; if it is lost,
every client must be re-issued.

```sh
tar czf wireguard-backup-$(date +%F).tgz -C ~/homelab/volume/infra wireguard
```

---

## Rollback

- **Revert the change**: revert the commit and merge, or *Run workflow* on *deploy* with
  an earlier `sha-` tag. If the service files are gone after the revert,
  `up -d --remove-orphans` removes the containers.
- **Stop the VPN only, leave the rest running**:

  ```sh
  cd ~/homelab
  docker compose -f docker-compose.yml -f docker-compose.prod.yml stop wireguard wg-ddns cloudflared
  ```

- A **Cloudflare outage** takes down only the admin UI — the WireGuard tunnel is direct
  UDP and keeps working (until the public IP changes, since `wg-ddns` also can't reach
  the Cloudflare API to update `WG_HOST`).
- The deploy health check only covers the `homelab` container, so a broken VPN service
  never blocks or auto-rolls-back a deploy — check it manually (above).

---

## Troubleshooting: "handshake did not complete"

The tunnel never comes up — the phone's UDP initiation isn't reaching the container, or
the reply isn't getting back. Work outward:

1. **Port match.** The client `Endpoint` port, `WG_UDP_PORT`, and the router's forwarded
   external port must be identical. `docker compose ... ps wireguard` shows the host
   mapping (`0.0.0.0:<WG_UDP_PORT>->51820/udp`); the router table and the client `.conf`
   must use that same `<WG_UDP_PORT>`.
2. **`WG_HOST` resolves to the house, not Cloudflare.** `dig +short <WG_HOSTNAME>` must
   return the server's public IPv4. Cloudflare ranges (`104.x`, `172.67.x`) mean the
   record is proxied — set it to *DNS only*. To bisect, put the raw public IP in the
   client `Endpoint` and retry.
3. **Packets arriving?** On the server, `sudo tcpdump -ni any udp port <WG_UDP_PORT>`,
   then connect from the phone **on cellular** (home Wi-Fi usually fails NAT hairpin).
   - Nothing arrives → router forward wrong, or the ISP is CGNAT (router WAN IP ≠
     `curl -s ifconfig.me`, or it's in `100.64.0.0/10` — port-forwarding can't work).
   - Arrives, no reply → check `docker exec homelab-wireguard-1 wg show` and the
     `WG_POST_UP` `INPUT ... --dport 51820 ACCEPT` rule.
4. **DNS resolves but IP is stale** — `wg-ddns` down or its token lacks `Zone:DNS:Edit`:
   `docker logs homelab-wg-ddns-1`.
