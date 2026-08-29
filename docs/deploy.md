# Deploying homelab to the dev server

## How it works

- Open a PR → `ci.yml` runs `go test`, validates the compose config, and builds
  the image (no push).
- Merge to `main` → `deploy.yml`:
  1. `build-push` (GitHub-hosted): builds the `linux/amd64,linux/arm64` image and
     pushes `ghcr.io/csperando/homelab:latest` and `:sha-<short>`.
  2. `deploy` (self-hosted runner on the dev server): syncs the compose files into
     `/opt/homelab`, writes `.env` from the `ENV` secret, `make deploy` (pull +
     `up -d`) pinned to the `sha-<short>` just built, then health-checks it.
- `registry-cleanup.yml` trims old `sha-*` tags weekly.

The server only ever runs the published image. **Never run `make build` / `make start`
on the server** — those rebuild locally.

## Prerequisites already present

- Docker Engine + Docker Compose v2.24+ (`docker compose version`).

## One-time server setup

1. **Get a runner registration token.** In the repo on GitHub:
   Settings → Actions → Runners → **New self-hosted runner**. Under "Configure",
   copy the token from the `--token` value (valid ~1 hour).

2. **Run the bootstrap script** on the dev server, as your normal login user:

   ```sh
   git clone https://github.com/csperando/homelab   # or copy just scripts/bootstrap-server.sh
   cd homelab
   RUNNER_TOKEN=<token from step 1> ./scripts/bootstrap-server.sh
   ```

   It installs `rsync`/`make`/`curl`, adds you to the `docker` group, enables
   `docker.service`, creates `/opt/homelab`, and installs the Actions runner as a
   service. Safe to re-run.

3. **Log fully out and back in** (SSH session included) so the `docker` group
   membership applies. Verify:

   ```sh
   id | grep -o docker      # -> docker
   docker ps                 # succeeds without sudo
   ```

4. **(Optional) `docker login ghcr.io`** with a Personal Access Token that has the
   `read:packages` scope — only needed if you want to `docker pull` / roll back
   manually on the box. The deploy workflow itself uses `GITHUB_TOKEN` and needs
   no PAT.

## One-time GitHub setup

- **`ENV` secret** — already configured (full contents of `.env`: Postgres
  credentials + `DOCKER_SOCK_PATH`). The deploy job writes it to
  `/opt/homelab/.env` (mode 600) on every run.
- **`GHCR_CLEANUP_TOKEN` secret** *(optional)* — a PAT with `packages:write`, used
  by `registry-cleanup.yml`. Until it is set, that workflow logs a warning and
  does nothing.
- **Package visibility** — the image is private by default because the repo is
  private. Confirm at `https://github.com/users/csperando/packages/container/homelab/settings`.
- **Branch protection** — see below.

## Branch protection on `main`

Settings → Branches → **Add branch ruleset** (or classic "Add rule") for `main`:

- Require a pull request before merging (no required reviewers — self-merge is fine).
- Require status checks to pass → add **`test`** (the job in `ci.yml`).

Optional `gh` equivalent:

```sh
gh api -X PUT repos/csperando/homelab/branches/main/protection --input - <<'JSON'
{
  "required_status_checks": { "strict": true, "contexts": ["test"] },
  "enforce_admins": false,
  "required_pull_request_reviews": { "required_approving_review_count": 0 },
  "restrictions": null
}
JSON
```

## Day to day

- **Watch a deploy**: repo → Actions → **deploy** → latest run. The `deploy` job's
  summary shows the deployed tag, the previous tag, and the health result.
- **Verify on the server**:

  ```sh
  cd /opt/homelab
  docker compose -f docker-compose.yml -f docker-compose.prod.yml ps
  curl -fsS localhost:55123/healthz
  ```

## Rollback / manual redeploy

- **From GitHub**: Actions → deploy → **Run workflow** → set **tag** to an existing
  image tag, e.g. `sha-abc1234`. This skips the build and redeploys that tag.
- **On the server**:

  ```sh
  make -C /opt/homelab deploy HOMELAB_TAG=sha-abc1234
  ```

  Available tags: `https://github.com/users/csperando/packages/container/homelab/versions`.

## Recovery: `build-push` succeeded but `deploy` failed

The image is already in GHCR; only the server didn't update. Options:

- Re-run just the failed `deploy` job: Actions → the run → **Re-run failed jobs**.
- Or trigger `deploy.yml` via **Run workflow** with **tag** set to the `sha-<short>`
  from the failed run (shown in the `build-push` logs / job summary).
- Or on the server: `make -C /opt/homelab deploy HOMELAB_TAG=<that sha tag>`.

If the health check failed, the job summary and `::error::` line carry the previous
tag and the exact rollback command; `docker logs homelab` on the server has details.
