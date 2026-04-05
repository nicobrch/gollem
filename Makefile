.PHONY: help env-docker up down logs logs-gateway ps rebuild restart

COMPOSE ?= docker compose
ENV_FILE ?= .env.docker
ENV_EXAMPLE ?= .env.docker.example

help:
	@printf "Available targets:\n"
	@printf "  make env-docker   Create %s from %s if missing\n" "$(ENV_FILE)" "$(ENV_EXAMPLE)"
	@printf "  make up           Build and start gateway + postgres + redis\n"
	@printf "  make down         Stop and remove compose services\n"
	@printf "  make logs         Tail logs for all services\n"
	@printf "  make logs-gateway Tail logs for gateway service\n"
	@printf "  make ps           Show service status\n"
	@printf "  make rebuild      Rebuild images and restart services\n"
	@printf "  make restart      Restart running services\n"

env-docker:
	@if [ ! -f "$(ENV_FILE)" ]; then \
		cp "$(ENV_EXAMPLE)" "$(ENV_FILE)"; \
		printf "Created %s from %s\n" "$(ENV_FILE)" "$(ENV_EXAMPLE)"; \
	else \
		printf "%s already exists\n" "$(ENV_FILE)"; \
	fi

up: env-docker
	$(COMPOSE) --env-file "$(ENV_FILE)" up --build -d

down:
	$(COMPOSE) --env-file "$(ENV_FILE)" down

logs:
	$(COMPOSE) --env-file "$(ENV_FILE)" logs -f

logs-gateway:
	$(COMPOSE) --env-file "$(ENV_FILE)" logs -f gateway

ps:
	$(COMPOSE) --env-file "$(ENV_FILE)" ps

rebuild:
	$(COMPOSE) --env-file "$(ENV_FILE)" up --build -d --force-recreate

restart:
	$(COMPOSE) --env-file "$(ENV_FILE)" restart
