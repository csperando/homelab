CONTAINER := homelab
WORKSPACE ?= $(CURDIR)/volume

.PHONY: build run stop restart shell logs clean test

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
