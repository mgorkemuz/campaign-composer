.PHONY: up down run web test test-go test-web vet build session

up:
	docker compose up --build

down:
	docker compose down -v

# Run the API locally (embedded Postgres when DATABASE_URL is unset). Serves
# web/dist if it has been built; use `make web` for the dev server with reload.
run:
	go run ./cmd/server

web:
	cd web && npm install && npm run dev

build:
	go build -o bin/server ./cmd/server
	cd web && npm ci && npm run build

# All tests. No API key, Docker or network needed (Postgres is embedded).
test: test-go test-web

test-go:
	go test -race ./...

test-web:
	cd web && (test -d node_modules || npm ci) && npm test

vet:
	go vet ./...
	cd web && npx vue-tsc --noEmit

# Scrub a Claude app session export into ai-sessions/.
# Usage: make session IN=~/Downloads/session-export-123.zip NAME=01-scaffold
session:
	@test -n "$(IN)" || (echo "usage: make session IN=<export.zip> NAME=<name>" && exit 2)
	go run ./tools/sessionexport -in "$(IN)" -name "$(NAME)"
