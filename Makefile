.PHONY: test race integration build debugger-test debugger-build stack-up stack-stop smoke model-smoke first-contact benchmark classic-benchmark run fmt aml-test aml-build aml-smoke aml-bench

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

model-smoke: build
	python3 scripts/model_smoke.py

first-contact: build
	python3 scripts/first_contact.py

benchmark: build
	python3 scripts/benchmark.py

classic-benchmark: build
	python3 scripts/benchmark.py --side classic

run:
	go run ./cmd/temporality-runtime

aml-test:
	go test ./aml/...

aml-build:
	go build -o bin/aml-bench ./cmd/aml-bench

aml-smoke: aml-build
	./bin/aml-bench --fixture main --sessions 2 --flip-at 0 --tasks T1,T3 --time-limit 20m \
		--report docs/benchmarks/aml-smoke.md --dump benchmarks/aml-smoke.json

aml-bench: aml-build
	./bin/aml-bench --fixture main

AML3_ENV = set -a; . ./.env; set +a;

aml3-test:
	go test ./aml/coding/... ./aml/sidecar/... ./aml/memory/...

aml3-smoke:
	$(AML3_ENV) go run ./cmd/aml-coding-bench --phase 3a --sessions-3a 1 --arms D \
		--tasks T1,T10 --time-limit 30m --out benchmarks/aml3-smoke --work /tmp/aml3-smoke

aml3-bench:
	$(AML3_ENV) go run ./cmd/aml-coding-bench --phase all --time-limit 300m \
		--out benchmarks/aml3 --work /tmp/aml3

aml3-sidecar:
	$(AML3_ENV) go run ./cmd/aml-sidecar --world coding --addr 127.0.0.1:18190

fmt:
	gofmt -w cmd frp
