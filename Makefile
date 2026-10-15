.PHONY: build test lint docker-build check

build:
	CGO_ENABLED=0 go build -o bin/zenmoney-mcp ./cmd/zenmoney-mcp

test:
	go vet ./...
	go test ./...

lint:
	golangci-lint run

docker-build:
	docker compose build

# Один синк с токеном из .env через контейнер: проверка токена, сети и volume.
check:
	docker compose run --rm -T zenmoney-mcp --check
