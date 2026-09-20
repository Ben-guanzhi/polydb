.PHONY: all gen-protocol check-protocol check-rust check-go check-web test-web lint-web contract-test clean docker-build docker-build-rust docker-run compose-up compose-up-rust compose-down

all: check-protocol check-rust check-go

# ─── Protocol Generation ──────────────────────────────────
# ts        —— spec/ → web/src/api/index.ts（--check 仅校验不写回）
# check     —— spec ↔ Rust ↔ Go wire 形态比对，漂移即失败
gen-protocol:
	cd tools/genprotocol && go run . ts

check-protocol:
	cd tools/genprotocol && go run . ts --check
	cd tools/genprotocol && go run . check

# ─── Rust ─────────────────────────────────────────────────
check-rust:
	cd rust && cargo check --workspace

build-rust:
	cd rust && cargo build --workspace

test-rust:
	cd rust && cargo test --workspace

fmt-rust:
	cd rust && cargo fmt --all

clippy-rust:
	cd rust && cargo clippy --workspace -- -D warnings

# ─── Go ───────────────────────────────────────────────────
check-go:
	cd go && go build ./...

test-go:
	cd go && go test ./...

lint-go:
	cd go && golangci-lint run ./...

fmt-go:
	cd go && gofmt -w . && goimports -w .

run-tui:
	cd go && go run ./cmd/polydb-tui

# M17：MCP server（stdio，供 Cursor / Claude Desktop 连接；只读工具）
run-mcp:
	cd go && go run ./cmd/polydb-mcp

# ─── Web / TypeScript ─────────────────────────────────────
check-web:
	cd web && npm run check

test-web:
	cd web && npm run test

lint-web:
	cd web && npm run lint

build-web:
	cd web && npm run build

# ─── Contract Tests ───────────────────────────────────────
contract-test: build-rust
	cd test/contract && go test -v ./...

# ─── Docker（M7）───────────────────────────────────────────
docker-build:
	docker build -t polydb-server:latest .

docker-build-rust:
	docker build -t polydb-server-rust:latest -f rust/Dockerfile.server .

docker-run:
	docker run --rm -p 8080:8080 -v polydb-data:/data polydb-server:latest

compose-up:
	docker compose up -d --build

compose-up-rust:
	docker compose --profile rust up -d --build

compose-down:
	docker compose down

# ─── Clean ────────────────────────────────────────────────
clean:
	cd rust && cargo clean
	cd go && go clean ./...
	rm -rf web/dist
