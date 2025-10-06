.PHONY: gateway orchestrator workflow admin webhook runners lint

gateway:
	GOCACHE=$(PWD)/.gocache CGO_ENABLED=0 go build ./cmd/gateway

workflow:
	GOCACHE=$(PWD)/.gocache CGO_ENABLED=0 go build ./cmd/workflow-engine

admin:
	GOCACHE=$(PWD)/.gocache CGO_ENABLED=0 go build ./cmd/admin-rest

webhook:
	GOCACHE=$(PWD)/.gocache CGO_ENABLED=0 go build ./cmd/webhook-dispatcher

control-plane:
	GOCACHE=$(PWD)/.gocache CGO_ENABLED=0 go build ./cmd/control-plane

orchestrator:
	cargo build -p orchestrator

runners:
	poetry install --directory runners

lint:
	golangci-lint run ./...
