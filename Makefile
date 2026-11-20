.PHONY: up down run test vet build session

up:
	docker compose up --build

down:
	docker compose down -v

run:
	go run ./cmd/server

build:
	go build -o bin/server ./cmd/server

test:
	go test -race ./...

vet:
	go vet ./...

# Scrub a Claude app session export into ai-sessions/.
# Usage: make session IN=~/Downloads/session-export-123.zip NAME=01-scaffold
session:
	@test -n "$(IN)" || (echo "usage: make session IN=<export.zip> NAME=<name>" && exit 2)
	go run ./tools/sessionexport -in "$(IN)" -name "$(NAME)"
