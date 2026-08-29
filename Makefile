CONTAINER := homelab
WORKSPACE ?= $(CURDIR)/volume

# Pull-based deploy (servers): run the published image, never build locally.
PROD_COMPOSE := -f docker-compose.yml -f docker-compose.prod.yml
HOMELAB_TAG ?= latest
export HOMELAB_TAG

.PHONY: build run stop restart shell logs clean test deploy prod-config

build:
	docker compose build

test:
	cd healthcheck && go test -v ./...

run:
	@if [ ! -f .env ]; then \
		cp .env.sample .env; \
		echo "No .env found — seeded one from .env.sample."; \
	fi
	mkdir -p $(WORKSPACE)
	WORKSPACE=$(WORKSPACE) docker compose up -d

start: clean build run

stop:
	docker compose stop

restart: stop run

shell:
	docker exec -it $(CONTAINER) bash

logs:
	docker compose logs -f homelab

clean:
	docker compose down

# Servers use this — pull the published image and (re)start the stack. Never run
# `make build` / `make start` on a server; those rebuild the image locally.
# Override the deployed tag with `make deploy HOMELAB_TAG=sha-abc1234`.
deploy:
	@if [ ! -f .env ]; then \
		echo "error: .env not found — deploy needs the real credentials file, not a seeded sample" >&2; \
		exit 1; \
	fi
	docker compose $(PROD_COMPOSE) pull
	docker compose $(PROD_COMPOSE) up -d --remove-orphans

prod-config:
	docker compose $(PROD_COMPOSE) config
