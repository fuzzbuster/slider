COMPOSE ?= docker compose
COMPOSE_ENV ?= .env
COMPOSE_FILE := compose.yaml
COMPOSE_PROJECT ?= slider
COMPOSE_ARGS := --env-file $(COMPOSE_ENV) -f $(COMPOSE_FILE) -p $(COMPOSE_PROJECT)

.PHONY: deploy-init deploy-check-env deploy-config deploy-build deploy-up deploy-down deploy-pull deploy-ps deploy-logs

deploy-init:
	@if [ -e "$(COMPOSE_ENV)" ]; then \
		echo "$(COMPOSE_ENV) already exists"; \
	else \
		cp ".env.example" "$(COMPOSE_ENV)"; \
		chmod 600 "$(COMPOSE_ENV)"; \
		echo "Created $(COMPOSE_ENV). Edit it before starting production."; \
	fi

deploy-check-env:
	@if [ ! -f "$(COMPOSE_ENV)" ]; then \
		echo "Missing $(COMPOSE_ENV). Run 'make deploy-init' and edit it."; \
		exit 1; \
	fi

deploy-config: deploy-check-env
	$(COMPOSE) $(COMPOSE_ARGS) config --quiet

deploy-build: deploy-check-env
	$(COMPOSE) $(COMPOSE_ARGS) build

deploy-up: deploy-check-env
	$(COMPOSE) $(COMPOSE_ARGS) up -d --build

deploy-down: deploy-check-env
	$(COMPOSE) $(COMPOSE_ARGS) down

deploy-pull: deploy-check-env
	$(COMPOSE) $(COMPOSE_ARGS) pull

deploy-ps: deploy-check-env
	$(COMPOSE) $(COMPOSE_ARGS) ps

deploy-logs: deploy-check-env
	$(COMPOSE) $(COMPOSE_ARGS) logs --tail=100 certgen slider caddy
