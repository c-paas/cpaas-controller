GOCMD=go
GOTEST=$(GOCMD) test
BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
HASH := $(shell git rev-parse --short HEAD)
GREEN  := $(shell tput -Txterm setaf 2)
YELLOW := $(shell tput -Txterm setaf 3)
WHITE  := $(shell tput -Txterm setaf 7)
CYAN   := $(shell tput -Txterm setaf 6)
RESET  := $(shell tput -Txterm sgr0)

.PHONY: all test build clean run generate-crd

PROJECT_NAME := cpaas-controller
CONTROLLER_GEN_VERSION := v0.17.2

all: help

## Generate CRDs:
generate-crd: ## Generate all CRDs for out custom resources
	@./bin/controller-gen crd:maxDescLen=0 paths="./..." output:crd:dir=./artifacts/crd

## Build:
build: ## Build all the binaries and put the output
	$(GOCMD) build -ldflags "-X main.version=$(BRANCH)-$(HASH)" -o $(PROJECT_NAME) .

## Deploy:
deploy: ## Deploy all CRD's and example CR to current cluster
	@kubectl apply -f artifacts/crd/
	@kubectl apply -f artifacts/srv.yaml
	@kubectl apply -f artifacts/example-cpaas.yaml

## Clean:
clean: ## Remove build related file
	@-rm -fr ./$(PROJECT_NAME)

## Run:
run: clean build ## Run `make run`
	./$(PROJECT_NAME) -kubeconfig=$(HOME)/.kube/config

## Test:
test: ## Run the tests
	$(GOTEST) -v -race ./...

## Deps:
deps: ## Install deps
	@go mod vendor
	@go build -mod vendor -o bin/controller-gen sigs.k8s.io/controller-tools/cmd/controller-gen

## Help:
help: ## Show this help.
	@echo ''
	@echo 'Usage:'
	@echo '  ${YELLOW}make${RESET} ${GREEN}<target>${RESET}'
	@echo ''
	@echo 'Targets:'
	@awk 'BEGIN {FS = ":.*?## "} { \
		if (/^[a-zA-Z_-]+:.*?##.*$$/) {printf "    ${YELLOW}%-20s${GREEN}%s${RESET}\n", $$1, $$2} \
		else if (/^## .*$$/) {printf "  ${CYAN}%s${RESET}\n", substr($$1,4)} \
		}' $(MAKEFILE_LIST)