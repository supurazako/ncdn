SHELL := /bin/bash

GO ?= go
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0
DIST_ROOT ?= dist/$(GOOS)-$(GOARCH)
GO_BUILD := CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -ldflags='-s -w'

.DEFAULT_GOAL := help

.PHONY: help
help: ## 利用可能なtargetを表示する
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "%-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: deploy
deploy: deploy-l4lb deploy-l7lb deploy-origin ## Linux amd64向けの全roleをビルドする

.PHONY: deploy-l4lb
deploy-l4lb: ## L4LB本体とfull版XDP objectをビルドする
	@mkdir -p "$(DIST_ROOT)/l4lb"
	$(GO_BUILD) -o "$(DIST_ROOT)/l4lb/l4lb" ./l4lb/cmd
	$(MAKE) --no-print-directory -C l4lb/c L4LB_VARIANT=full NCDN_STRIP=0 lb.o
	cp l4lb/c/lb.o "$(DIST_ROOT)/l4lb/lb-full.o"
	cp deploy/physical/l4lb/README.md "$(DIST_ROOT)/l4lb/README.md"
	cp deploy/physical/l4lb/inspect.sh "$(DIST_ROOT)/l4lb/inspect.sh"
	chmod +x "$(DIST_ROOT)/l4lb/inspect.sh"

.PHONY: deploy-l7lb
deploy-l7lb: ## L7LB兼cache serverをビルドする
	@mkdir -p "$(DIST_ROOT)/l7lb"
	$(GO_BUILD) -o "$(DIST_ROOT)/l7lb/l7lb" ./popcache
	cp deploy/physical/l7lb/README.md \
		deploy/physical/l7lb/ncdn-l7lb.service \
		deploy/physical/l7lb/ncdn-l7lb.default.example \
		deploy/physical/l7lb/journald-persistent.conf.example \
		"$(DIST_ROOT)/l7lb/"

.PHONY: deploy-origin
deploy-origin: ## Origin serverをビルドする
	@mkdir -p "$(DIST_ROOT)/origin"
	$(GO_BUILD) -o "$(DIST_ROOT)/origin/origin" ./origin
	cp -R origin/templates origin/static "$(DIST_ROOT)/origin/"
	cp deploy/physical/origin/README.md "$(DIST_ROOT)/origin/README.md"

.PHONY: l4lb-variants
l4lb-variants: ## 性能比較用の全XDP variantをビルドする
	$(MAKE) --no-print-directory -C l4lb/c variants

.PHONY: clean
clean: ## 生成した配布物とBPF中間生成物を削除する
	rm -rf dist
	$(MAKE) --no-print-directory -C l4lb/c clean

DEVCONTAINER ?= $(shell command -v devcontainer 2>/dev/null || printf '%s' "$$HOME/.devcontainers/bin/devcontainer")

.PHONY: devcontainer-up
devcontainer-up: ## devcontainerを起動する
	$(DEVCONTAINER) up --workspace-folder "$(CURDIR)"

.PHONY: devcontainer-shell
devcontainer-shell: ## devcontainer内のshellを開く
	$(DEVCONTAINER) exec --workspace-folder "$(CURDIR)" bash
