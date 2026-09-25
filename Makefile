.PHONY: test race build debugger-test debugger-build stack-up stack-stop journal-run kernel-run fmt

test:
	go test ./...

race:
	go test -race ./...

build:
	mkdir -p bin
	go build -o bin/temporality-journal ./cmd/temporality-journal
	go build -o bin/temporality-agent-kernel ./cmd/agent-kernel

debugger-test:
	npm --prefix debugger test

debugger-build:
	npm --prefix debugger run build

stack-up:
	docker compose up --build -d

stack-stop:
	docker compose stop journal debugger agent-kernel

journal-run:
	go run ./cmd/temporality-journal

kernel-run:
	go run ./cmd/agent-kernel

fmt:
	gofmt -w cmd kernel observation examples
