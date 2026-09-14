.PHONY: test race integration build debugger-test debugger-build stack-up stack-stop smoke run fmt

test:
	go test ./...

race:
	go test -race ./...

integration:
	@test -n "$(TEST_DATABASE_URL)" || (echo "TEST_DATABASE_URL is required" && exit 1)
	go test -count=1 ./frp/substrate/postgres

debugger-test:
	npm --prefix debugger test

debugger-build:
	npm --prefix debugger run build

stack-up:
	docker compose up --build -d

stack-stop:
	docker compose stop runtime executor debugger

build:
	mkdir -p bin
	go build -o bin/temporality-runtime ./cmd/temporality-runtime
		go build -o bin/temporality-executor ./cmd/temporality-executor

smoke: build
	python3 scripts/smoke.py

run:
	go run ./cmd/temporality-runtime

fmt:
	gofmt -w cmd frp
