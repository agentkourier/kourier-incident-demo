IMAGE ?= demo-mcp:dev
VERSION ?= dev

.PHONY: help fmt vet test build image render

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

fmt: ## gofmt the module
	gofmt -w .

vet: ## go vet
	go vet ./...

test: vet ## vet, then race-enabled tests
	go test -race ./...

build: ## build one binary per server into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/ ./...

image: ## build the $(IMAGE) container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

render: ## render the Kubernetes manifests
	kubectl kustomize deploy
