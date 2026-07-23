# homelab

A local Docker dev environment: Go, Node/pnpm, and Claude Code preinstalled, with a
persistent workspace and a built-in health API.

## Usage

Via `make`:

```sh
make build   # build the image
make run     # start (or resume) the container
make shell   # open a shell in the running container
make stop    # stop the container
make restart # stop + run
make logs    # follow container logs
make clean   # remove the container
```

Via Docker Compose:

```sh
docker compose build
docker compose up -d
docker compose exec homelab bash
docker compose down
```

Both paths mount `./volume` (in this repo) to `/root/workspace` in the container, so
cloned repos and work survive container restarts/rebuilds. Its contents are gitignored.
Override the host path with a `WORKSPACE` variable, e.g. `WORKSPACE=~/code make run` or
`WORKSPACE=~/code docker compose up -d`.

## Health API

The container runs a small Go service on port `55123`:

```sh
curl localhost:55123/healthz
```

Returns JSON with status, uptime, installed tool versions (Go, Node, npm, pnpm, git), and
disk usage for `/root/workspace`. Docker's own `HEALTHCHECK` polls this endpoint, so
`docker ps` / `docker compose ps` reflect container health directly.

## Ports

- `5173` — frontend dev server (e.g. Vite/Vue)
- `55123` — dashboard/health API
