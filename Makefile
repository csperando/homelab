IMAGE := homelab
CONTAINER := homelab
WORKSPACE ?= $(CURDIR)/volume

.PHONY: build run stop restart shell logs clean

build:
	docker build -t $(IMAGE) .

run:
	@if [ -n "$$(docker ps -aq -f name=^/$(CONTAINER)$$)" ]; then \
		docker start $(CONTAINER); \
	else \
		mkdir -p $(WORKSPACE); \
		docker run -dit \
			--name $(CONTAINER) \
			-p 5173:5173 \
			-p 8080:8080 \
			-v $(WORKSPACE):/root/workspace \
			$(IMAGE); \
	fi

stop:
	docker stop $(CONTAINER)

restart: stop run

shell:
	docker exec -it $(CONTAINER) bash

logs:
	docker logs -f $(CONTAINER)

clean:
	-docker rm -f $(CONTAINER)
