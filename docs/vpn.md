# VPN runbook (wg-easy + cloudflared)

The `wireguard` (wg-easy v14) and `cloudflared` infra services provide a WireGuard VPN.
Both are gated behind the `vpn` compose profile, so they run **only on the server**, which
activates them with `COMPOSE_PROFILES=vpn` in its `.env` (via the `ENV` secret).

**Model**

- **WireGuard UDP endpoint** — the only inbound port. Published on `WG_UDP_PORT`
  (default 51820) and forwarded on the router. WireGuard does not answer unauthenticated
  packets, so this is a quiet surface.
- **wg-easy admin UI** — no inbound port. `cloudflared` dials out to Cloudflare; a
  Cloudflare Tunnel routes a public hostname to `http://wireguard:${WG_UI_PORT}`, with
  **Cloudflare Access** enforcing login at the edge. wg-easy keeps its own login behind
  Access.
- **Full tunnel** — clients get `WG_ALLOWED_IPS=0.0.0.0/0, ::/0`; all their traffic
  egresses through the server. `WG_POST_UP` in `services/wireguard/compose.yml` drops
  forwarded `wg0` traffic to every RFC1918 range and blocks the UI port on `wg0`, so a
  client (or a stolen client key) is a bare internet exit — no route to the container
  network, the home LAN, or the admin UI.

> Keep real hostnames, public IPs, LAN CIDRs, the tunnel token, and the password hash out
> of every tracked file. They live only in the `ENV` secret / the server's `.env`.
> Placeholders below: `<VPN_HOSTNAME>`, `<UDP_PORT>`.

---

## One-time setup

### 1. Cloudflare (dashboard)

1. **Zero Trust** — enable it on the account (Cloudflare dashboard → Zero Trust), pick a
   team name, choose the free plan.
2. **Create the tunnel** — Zero Trust → Networks → Tunnels → *Create a tunnel* →
   *Cloudflared* → name it → **copy the token** (`eyJ...`).
3. **Public hostname** — on the tunnel, *Public Hostname* → *Add*:
   - Subdomain/domain: `<VPN_HOSTNAME>`
   - Service: `HTTP` → `wireguard:51821` (use the `WG_UI_PORT` value)
   - This writes the DNS record automatically. Remove any old A record for that name.
4. **Access application** — Zero Trust → Access → Applications → *Add an application* →
   *Self-hosted*:
   - Application domain: `<VPN_HOSTNAME>`
   - Policy: *Allow* → include your email(s); identity method one-time PIN (no IdP setup)
     or an SSO provider.
5. **Router** — forward inbound `UDP <UDP_PORT>` to the server's LAN IP. No other inbound
   ports.

### 2. Secrets → the `ENV` GitHub secret

Add to the `ENV` secret (repo → Settings → Secrets and variables → Actions → `ENV`):

```
COMPOSE_PROFILES=vpn
WG_HOST=<VPN_HOSTNAME>
WG_UDP_PORT=<UDP_PORT>
WG_UI_PORT=51821
WG_DEFAULT_DNS=1.1.1.1
WG_ALLOWED_IPS=0.0.0.0/0, ::/0
PASSWORD_HASH=<hash from below>
TUNNEL_TOKEN=<token from step 2>
```

Without `COMPOSE_PROFILES=vpn` the deploy's `up -d --remove-orphans` will delete the two
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
docker compose -f docker-compose.yml -f docker-compose.prod.yml ps      # wireguard + cloudflared up
docker logs --tail 20 cloudflared                                       # "Registered tunnel connection"
docker exec wireguard wg show
```

---

## Adding a user

1. Open `https://<VPN_HOSTNAME>`, pass Cloudflare Access, then the wg-easy login.
2. *New Client* → name it → download the `.conf` or show the QR.
3. Send it to the user out of band.

**End-user note to include:**

> Install the official **WireGuard** app (not OpenVPN). Import the `.conf` file, or scan
> the QR from the app. While the tunnel is on, *all* your traffic — and your public IP —
> routes through the home connection; turn it off for normal browsing speed.

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
  docker compose -f docker-compose.yml -f docker-compose.prod.yml stop wireguard cloudflared
  ```

- A **Cloudflare outage** takes down only the admin UI — the WireGuard tunnel is direct
  UDP and keeps working.
- The deploy health check only covers the `homelab` container, so a broken VPN service
  never blocks or auto-rolls-back a deploy — check it manually (above).
