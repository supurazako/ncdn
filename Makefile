SHELL := /bin/bash

WORKSPACE := $(abspath .)
DEVCONTAINER ?= $(shell command -v devcontainer 2>/dev/null || printf '%s' "$$HOME/.devcontainers/bin/devcontainer")
DEVCONTAINER_LABEL := devcontainer.local_folder=$(WORKSPACE)

.PHONY: help
help: ## Show available targets.
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*## "}; {printf "%-24s %s\n", $$1, $$2}'

.PHONY: devcontainer-up
devcontainer-up: ## Create or start the devcontainer.
	$(DEVCONTAINER) up --workspace-folder "$(WORKSPACE)"

.PHONY: devcontainer-rebuild
devcontainer-rebuild: ## Recreate the devcontainer from scratch.
	$(DEVCONTAINER) up --workspace-folder "$(WORKSPACE)" --remove-existing-container

.PHONY: devcontainer-shell
devcontainer-shell: ## Open bash inside the running devcontainer.
	$(DEVCONTAINER) exec --workspace-folder "$(WORKSPACE)" bash

.PHONY: devcontainer-ps
devcontainer-ps: ## Show the devcontainer Docker container for this workspace.
	@docker ps -a --filter "label=$(DEVCONTAINER_LABEL)" --format 'table {{.ID}}\t{{.Status}}\t{{.Names}}\t{{.Image}}'

.PHONY: devcontainer-stop
devcontainer-stop: ## Stop the devcontainer for this workspace.
	@ids="$$(docker ps -q --filter "label=$(DEVCONTAINER_LABEL)")"; \
	if [ -n "$$ids" ]; then docker stop $$ids; else echo "No running devcontainer for $(WORKSPACE)"; fi

.PHONY: devcontainer-rm
devcontainer-rm: ## Remove the devcontainer for this workspace.
	@ids="$$(docker ps -aq --filter "label=$(DEVCONTAINER_LABEL)")"; \
	if [ -n "$$ids" ]; then docker rm -f $$ids; else echo "No devcontainer for $(WORKSPACE)"; fi

.PHONY: dc-up dc-rebuild dc-shell dc-ps dc-stop dc-rm
dc-up: devcontainer-up
dc-rebuild: devcontainer-rebuild
dc-shell: devcontainer-shell
dc-ps: devcontainer-ps
dc-stop: devcontainer-stop
dc-rm: devcontainer-rm
