# Deploying homelab to the dev server

How the homelab image gets built, published, and run on the always-on dev server.
For the reasoning behind these choices, see [`deployment-analysis.md`](deployment-analysis.md).

**TL;DR** — merge to `main` and the server updates itself. First-time server setup is one
script: `RUNNER_TOKEN=<token> ./scripts/bootstrap-server.sh`, then a re-login.

---

## How it works

- **Open a PR** → `ci.yml` (GitHub-hosted): `go test`, `docker compose config` validation,
  and a no-push image build.
- **Merge to `main`** → `deploy.yml`:
  1. `build-push` (GitHub-hosted) builds the `linux/amd64,linux/arm64` image and pushes
     `ghcr.io/csperando/homelab:latest` and `:sha-<short>`.
  2. `deploy` (self-hosted runner on the server) preflight-checks the host, `rsync`s the
     tracked compose/config files into `~/homelab`, writes `.env` from the `ENV` secret,
     runs `docker compose pull` + `up -d` pinned to the `sha-<short>` just built, then
     polls the container health check for up to 120 s.
- `registry-cleanup.yml` (weekly) prunes old `sha-*` tags, keeping `:latest` + the 15
  newest.

The server **only ever runs the published image**. Never run `make build` / `make start`
there — those rebuild locally. After a host reboot the stack and the runner come back on
their own (`restart: unless-stopped` + enabled services).

---

## Server requirements

- Debian/Ubuntu host (the bootstrap script uses `apt`).
- Docker Engine, and **Docker Compose v2.24+** (`docker compose version`) — the prod
  override uses the `!reset` tag.
- Outbound HTTPS to `github.com` and `ghcr.io`. **No inbound ports** — the runner dials
  out.

---

## One-time server setup

The deploy job runs as the runner's login user with **no `sudo`**. The only setup that
needs `sudo` — packages, the `docker` group, enabling Docker on boot — is done once by
`scripts/bootstrap-server.sh`, which also installs the Actions runner as a service.

> **Do not** follow the commands on GitHub's *Settings → Actions → Runners → New
> self-hosted runner* page. They end in `./run.sh`, which runs the runner in the
> foreground and stops when you close the terminal. `bootstrap-server.sh` does the same
> download + `config.sh` and then installs it as a **systemd service**.

Do this **before** the first merge to `main` — otherwise the `deploy` job sits queued
waiting for a runner.

1. **Get a runner registration token.** Repo → Settings → Actions → Runners → *New
   self-hosted runner* → Linux / x64. Copy the value after `--token` in the "Configure"
   box (valid ~1 hour). That page is only used for the token.

2. **Copy the script to the box and run it** (the repo is private, so `scp` one file
   rather than cloning):

   ```sh
   scp scripts/bootstrap-server.sh <user>@<server>:~/
   ssh <user>@<server>
   RUNNER_TOKEN=<token from step 1> ./bootstrap-server.sh
   ```

   It installs `rsync`/`make`/`curl`, adds you to the `docker` group, enables
   `docker.service`, creates `~/homelab`, downloads + registers the runner, and installs
   it as `actions.runner.<owner>-<repo>.<hostname>.service`. Idempotent — safe to re-run.
   After the first deploy a copy also lives at `~/homelab/scripts/bootstrap-server.sh`.

3. **Log fully out and back in** (new SSH session) so the `docker` group applies, then:

   ```sh
   sudo systemctl restart 'actions.runner.*'   # pick up the group
   id | grep -o docker                          # -> docker
   docker ps                                    # succeeds without sudo
   ```

   The runner now shows **Idle** in Settings → Actions → Runners.

4. **(Optional) `docker login ghcr.io`** with a classic PAT that has `read:packages` —
   only needed for manual `docker pull` / rollback *on the box*. The deploy workflow uses
   the automatic `GITHUB_TOKEN` and needs no PAT.

### Managing the runner

```sh
sudo ~/actions-runner/svc.sh status | stop | start
journalctl -u 'actions.runner.*' -f          # live logs
sudo ~/actions-runner/svc.sh uninstall       # remove the service (then "Remove" in repo settings)
```

---

## GitHub configuration

| Item | What | Notes |
|---|---|---|
| **`ENV`** secret | full contents of `.env` (Postgres creds + `DOCKER_SOCK_PATH`) | written to `~/homelab/.env` (mode 600) **every deploy** — edit the secret, not the file on the box |
| **`GHCR_CLEANUP_TOKEN`** secret *(optional)* | a **classic** PAT with `write:packages` + `delete:packages` | for `registry-cleanup.yml` only; fine-grained PATs have no packages write/delete permission. Inert (logs a warning) until set. Test: Actions → registry-cleanup → Run workflow → `dry-run = true` |
| Package visibility | private (inherited from the private repo) | confirm at `github.com/users/csperando/packages/container/homelab/settings` |
| Branch protection | **not enabled** — direct pushes to `main` are allowed | to require PRs later: Settings → Branches → require a PR (0 reviewers) + require the `test` check |

---

## Day to day

**Watch a deploy:** repo → Actions → *deploy* → latest run. The `deploy` job's summary
shows the deployed tag, the previous tag, and the health result.

**Check the server:**

```sh
cd ~/homelab
docker compose -f docker-compose.yml -f docker-compose.prod.yml ps
curl -fsS localhost:55123/healthz
```

---

## Rollback / manual redeploy

- **From GitHub:** Actions → *deploy* → *Run workflow* → set **tag** to an existing image
  tag (e.g. `sha-abc1234`). This skips the build and redeploys that tag.
- **On the server:**

  ```sh
  make -C ~/homelab deploy HOMELAB_TAG=sha-abc1234
  ```

Available tags: `github.com/users/csperando/packages/container/homelab/versions`.

---

## Troubleshooting

**`build-push` succeeded but `deploy` failed** — the image is in GHCR; only the server
didn't update.

- Re-run just the failed job: Actions → the run → *Re-run failed jobs*.
- Or *Run workflow* with **tag** set to the `sha-<short>` from the failed run.
- Or on the server: `make -C ~/homelab deploy HOMELAB_TAG=<that sha tag>`.

**`preflight` step fails** (`docker` not usable, `~/homelab` not writable, no
`docker compose`) — (re-)run `scripts/bootstrap-server.sh` and `sudo systemctl restart
'actions.runner.*'`.

**`deploy` job stays queued** — the runner is offline. `sudo ~/actions-runner/svc.sh
status`; start it, or re-run bootstrap.

**Health check failed** — the job summary and the `::error::` line carry the previous tag
and the exact rollback command. `docker logs homelab` on the server has the detail; the
old container kept running until the new image was pulled.
