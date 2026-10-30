DATABASE_URL ?= postgres://split:split@localhost:5432/split?sslmode=disable

.PHONY: test test-unit lint run up down

## test: all tests; Postgres integration tests need Docker and skip without it
test:
	go test -race -count=1 ./...

## test-unit: fast tests only
test-unit:
	go test -short ./...

lint:
	go vet ./...
	golangci-lint run ./...

## run: start the API against docker compose Postgres with the payment simulator
run:
	DATABASE_URL="$(DATABASE_URL)" WEBHOOK_SECRET=dev-secret PAYMENT_SIMULATOR=true go run ./cmd/api

up:
	docker compose up -d

down:
	docker compose down -v
