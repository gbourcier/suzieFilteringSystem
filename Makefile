.PHONY: tidy build test lint run dev-summarize docker-build up down pull-model

BINARY   := digestd
MODULE   := github.com/gbourcier/suzie
GOLANGCI := golangci-lint

tidy:
	go mod tidy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(BINARY) ./cmd/digestd

build-dev:
	CGO_ENABLED=0 go build -o bin/devsummarize ./cmd/devsummarize

test:
	go test ./...

lint:
	$(GOLANGCI) run ./...

run: build
	./bin/$(BINARY)

# Run the dev summarization harness against .fixtures/
# Usage: make dev-summarize
#        make dev-summarize MODEL=mistral-nemo LANG=en
MODEL  ?= qwen2.5:14b
LANG   ?= fr
REPORT ?= .fixtures/_report.md

dev-summarize: build-dev
	./bin/devsummarize \
		--fixtures .fixtures \
		--model $(MODEL) \
		--language $(LANG) \
		--show-body \
		--out $(REPORT)

docker-build:
	docker build -t suzie/digestd:local .

up:
	docker compose up -d

down:
	docker compose down

pull-model:
	docker compose exec ollama ollama pull $(MODEL)
