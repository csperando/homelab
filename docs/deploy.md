# Deploying homelab to the dev server

## How it works

- Open a PR → `ci.yml` runs `go test`, validates the compose config, and builds
  the image (no push).
- Merge to `main` → `deploy.yml`:
  1. `build-push` (GitHub-hosted): builds the `linux/amd64,linux/arm64` image and
     pushes `ghcr.io/csperando/homelab:latest` and `:sha-<short>`.
  2. `deploy` (self-hosted runner on the dev server): preflight-checks the host,
     syncs the compose files into `~/homelab`, writes `.env` from the `ENV` secret,
     runs `docker compose pull` + `up -d` pinned to the `sha-<short>` just built,
     then health-checks it.
- `registry-cleanup.yml` trims old `sha-*` tags weekly.

The server only ever runs the published image. **Never run `make build` / `make start`
on the server** — those rebuild locally.

## Prerequisites already present

- Docker Engine + Docker Compose v2 (`docker compose version`).

## One-time server setup

The deploy job runs as the runner's login user with no `sudo`. The only host setup
that needs `sudo` — packages, the `docker` group, enabling Docker on boot — is done
once by `scripts/bootstrap-server.sh`, which also installs the Actions runner as a
service.

1. **Get a runner registration token.** Repo → Settings → Actions → Runners →
   **New self-hosted runner** (Linux / x64). Copy the value after `--token` in the
   "Configure" box (valid ~1 hour).

2. **Copy the script to the box and run it** (the repo is private, so `scp` one file
   rather than cloning):

   ```sh
   scp scripts/bootstrap-server.sh <user>@<server>:~/
   ssh <user>@<server>
   RUNNER_TOKEN=<token from step 1> ./bootstrap-server.sh
   ```

   It installs `rsync`/`make`/`curl`, adds you to the `docker` group, enables
   `docker.service`, creates `~/homelab`, and registers the runner as a systemd
   service (`actions.runner.*`). Safe to re-run. After the first deploy a copy also
   lives at `~/homelab/scripts/bootstrap-server.sh`.

3. **Log fully out and back in** (new SSH session) so the `docker` group applies,
   then restart the runner so it picks up the group:

   ```sh
   sudo systemctl restart 'actions.runner.*'
   id | grep -o docker      # -> docker
   docker ps                 # succeeds without sudo
   ```

   The runner shows **Idle** in repo → Settings → Actions → Runners.

4. **(Optional) `docker login ghcr.io`** with a classic PAT that has `read:packages`
   — only for manual `docker pull` / rollback on the box. The deploy workflow uses
   `GITHUB_TOKEN` and needs no PAT.

## GitHub secrets

- **`ENV`** — already configured (full contents of `.env`: Postgres credentials +
  `DOCKER_SOCK_PATH`). The deploy job writes it to `~/homelab/.env` (mode 600) every run.
- **`GHCR_CLEANUP_TOKEN`** *(optional)* — a **classic** PAT with `write:packages` +
  `delete:packages` (fine-grained PATs have no packages write/delete permission), used
  by `registry-cleanup.yml`. Until it is set, that workflow logs a warning and does
  nothing. Test with Actions → registry-cleanup → Run workflow, `dry-run = true`.
- **Package visibility** — private by default (private repo). Confirm at
  `https://github.com/users/csperando/packages/container/homelab/settings`.

## Branch protection (not currently enabled)

`main` has no protection — direct pushes are allowed. To require PRs later:
Settings → Branches → Add rule for `main` → require a pull request (0 reviewers) and
require the **`test`** status check (`ci.yml`).

## Day to day

- **Watch a deploy**: repo → Actions → **deploy** → latest run. The `deploy` job's
  summary shows the deployed tag, the previous tag, and the health result.
- **Verify on the server**:

  ```sh
  cd ~/homelab
  docker compose -f docker-compose.yml -f docker-compose.prod.yml ps
  curl -fsS localhost:55123/healthz
  ```

## Rollback / manual redeploy

- **From GitHub**: Actions → deploy → **Run workflow** → set **tag** to an existing
  image tag, e.g. `sha-abc1234`. This skips the build and redeploys that tag.
- **On the server**:

  ```sh
  make -C ~/homelab deploy HOMELAB_TAG=sha-abc1234
  ```

  Available tags: `https://github.com/users/csperando/packages/container/homelab/versions`.

## Recovery: `build-push` succeeded but `deploy` failed

The image is already in GHCR; only the server didn't update. Options:

- Re-run just the failed `deploy` job: Actions → the run → **Re-run failed jobs**.
- Or trigger `deploy.yml` via **Run workflow** with **tag** set to the `sha-<short>`
  from the failed run.
- Or on the server: `make -C ~/homelab deploy HOMELAB_TAG=<that sha tag>`.

The deploy job's `preflight` step fails fast with a clear message if `docker` isn't
usable by the runner user or `~/homelab` isn't writable — the fix is always to
(re-)run `scripts/bootstrap-server.sh` and restart the runner. If the health check
failed, the job summary and `::error::` line carry the previous tag and the rollback
command; `docker logs homelab` on the server has details.
