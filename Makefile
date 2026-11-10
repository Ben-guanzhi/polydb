.PHONY: all gen-protocol check-protocol check-docs check-oracle check-rust check-go check-web test-web lint-web contract-test clean docker-build docker-build-rust docker-run compose-up compose-up-rust compose-down

all: check-protocol check-rust check-go

# ─── Protocol Generation ──────────────────────────────────
# ts        —— spec/ → web/src/api/index.ts（--check 仅校验不写回）
# check     —— spec ↔ Rust ↔ Go wire 形态比对，漂移即失败
# ops       —— spec/schemas/schemadiff-ops.json → Go/Rust op 表（--check 仅校验不写回）
gen-protocol:
	cd tools/genprotocol && go run . ts

gen-ops:
	cd tools/genprotocol && go run . ops

check-protocol:
	cd tools/genprotocol && go run . ts --check
	cd tools/genprotocol && go run . dispatch
	cd tools/genprotocol && go run . check
	cd tools/genprotocol && go run . ops --check
	cd tools/genprotocol && go run . docs

# docs gate：文档反引号里写出的 POLYDB_* 标识符（错误码、环境变量）必须能在
# 源码里找到定义。CI 的 drift check 已含此步，本地补上以堵住"本地绿 CI 红"。
check-docs:
	cd tools/genprotocol && go run . docs

# oracle gate：共享向量语料的**独立 oracle**（Item 9/10）。
# 三套语料的 expected 原本由某一端实现回写，两端互核对只能发现"不一致"，
# 不能发现"两端都错"。本门禁用**第三套推导**（Python，不读 Go/Rust 任一端
# 实现，只按语料 note 明示的规则从 source/target/selected 重算）比对语料，
# 因此能检出"两端共享的误解被回写成范式"的情况。
# 刻意用 Python 而非 Go：跨语言 ⇒ 不可能共享同一语言层面的误解。
#
# 规则集统一在 tools/oracle/schema_rules.py（两个 oracle 共用一份）。
#
# 覆盖：
#   select（18 份）—— 选中的 id 序列；
#   diff（17 份）  —— 完整计划条目，只显式排除 `reason` 文案；
#   ddl（27 份）   —— error_code + 正/反两向的**渲染条目集合**；
#                     不校验语句切分/顺序/相位（那是 SQLite 方言策略）、
#                     也不校验 `sql` 全文与 `warnings` 文案（渲染产物）。
#
# selftest 是元验证：篡改语料模拟"两端共享误解"，oracle 必须检出，
# 否则说明门禁是假绿。
check-oracle:
	python3 tools/oracle/select_vectors_oracle.py --check
	python3 tools/oracle/selftest.py
	python3 tools/oracle/diff_vectors_oracle.py --check
	python3 tools/oracle/diff_selftest.py
	python3 tools/oracle/ddl_vectors_oracle.py --check
	python3 tools/oracle/ddl_selftest.py

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
	cd tools/genprotocol && go run . ops --check
	cd test/contract && go test -v -timeout 30m ./...

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
