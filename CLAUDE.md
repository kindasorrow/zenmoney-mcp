# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`zenmoney-mcp` is a Go MCP server for read-only analytics of ZenMoney spending. It runs in Docker on a VPS, and Claude Code starts one container per session over stdio (`docker compose run --rm -T`). The server keeps a local SQLite copy of the account and answers questions about spending from it.

Read `README.md` first. It documents the tools, the calculation rules (expense, income, transfer, currencies, time zone) and how to install and connect the server. This file covers how to work in the repo.

## Commands

```bash
make build                    # bin/zenmoney-mcp, CGO_ENABLED=0
make test                     # go vet + go test
go test -race -count=1 ./...
make lint                     # golangci-lint v2, config .golangci.yml
golangci-lint fmt --diff      # gofmt + goimports
make docker-build
make check                    # one sync with the token from .env inside the container
```

Without Docker: `ZENMONEY_TOKEN=... ZENMONEY_DB=./zm.db ./bin/zenmoney-mcp --check`. The token comes from https://zerro.app/token.

## Structure

```
cmd/zenmoney-mcp     wiring, --check flag, stdio transport
internal/config      env: ZENMONEY_TOKEN, ZENMONEY_DB
internal/zenmoney    HTTP client for POST /v8/diff/ and the API DTOs
internal/store       GORM models, diff application, queries, Syncer (TTL, backoff), read-only SQL for query
internal/report      pure aggregates over report.Transaction, no DB access
internal/tools       MCP tool handlers and argument parsing
```

Data flows one way: `zenmoney` fetches a diff, `store` applies it and reads rows back as `report.Transaction`, `report` aggregates them, and `tools` turns the result into compact JSON.

## Invariants

These are the requirements the server was built around. Do not relax them without asking.

- Strictly read-only. The only API call is `POST /v8/diff/`, and `diffRequest` holds just the two timestamps. Never add entity fields to it, never add a tool or code that sends changes to ZenMoney.
- Network goes only to `https://api.zenmoney.ru`. Redirects are not followed, and there is no telemetry and no other host.
- The token is read only from `ZENMONEY_TOKEN`. It must never appear in logs, error texts or tool responses; `client_test.go` checks the error path.
- stdout carries the MCP protocol. Logs go to stderr through slog. GORM runs with `logger.Discard` because its default logger writes to stdout. The only stdout output outside the protocol is the `--check` result.
- The database file is created with mode 0600, and the WAL and SHM files must stay 0600 too (covered by a test).
- Every tool sets `readOnlyHint`.
- The `query` tool must never be able to change the cache or write files. It runs on a separate `mode=ro` connection, and `checkQuery` admits only a single `SELECT`/`WITH` statement, which rules out `ATTACH`, `PRAGMA`, `VACUUM INTO` and chains through `;`. Both layers are tested in `query_test.go`.
- Amounts in different currencies are never summed. Aggregates are grouped by currency, with RUB first.
- A transaction with both `income > 0` and `outcome > 0` is a transfer and counts as neither expense nor income.
- Use only public sources: Docker Hub, the official `golang` and distroless images, proxy.golang.org. No private registries, internal base images, `GOPROXY`/`GOPRIVATE` settings, or names and paths from the employer's infrastructure in code, Dockerfile, compose, `go.mod` or README.
- Ask before adding any dependency. The direct ones are the official MCP SDK, `gorm.io/gorm`, `github.com/glebarez/sqlite` and its driver `github.com/glebarez/go-sqlite` (imported directly to register SQL functions).

## Design decisions

- SQLite goes through `glebarez/sqlite` on the pure-Go `modernc.org/sqlite` engine, so there is no cgo for builds or tests. Do not switch to `gorm.io/driver/sqlite`, which needs cgo (`mattn/go-sqlite3`).
- The schema comes from `AutoMigrate`, with no migration tool. The database is a cache, so after an incompatible schema change the volume is deleted and synced again. `migrate()` retries once because two containers can start on an empty volume at the same time.
- Several Claude sessions mean several containers on one file. The DSN therefore sets WAL, `busy_timeout` and `_txlock=immediate`, and `Apply` skips a diff older than the stored `server_timestamp`, so a slow container cannot roll the data back.
- The `category` and `payee` filters run in Go (`containsFold`), and `payee` matches both the merchant title and the raw payee from the bank statement.
- SQLite without ICU folds case only for Latin letters, so `internal/store/casefold.go` overrides `lower`, `upper` and `like` with Unicode-aware versions for every connection. Without them `category LIKE '%еда%'` in `query` would silently miss «Еда».
- `query` reads the `operations` view, which repeats the rules of `report.Transaction` (kind, amount, currency) in SQL. `migrate()` recreates the view on every start, and `TestOperationsView_MatchesReport` keeps the two in sync: change both together.
- The driver interrupts a statement on context cancel only inside `Exec`, not while stepping `rows.Next`. That is why `Query` computes the result with `CREATE TEMP TABLE ... AS` under `QueryTimeout`, with a `LIMIT` that keeps the temporary table small.
- "Today" and default periods use the container's local time. Compose sets `TZ` to `Europe/Moscow` by default.
- `Syncer` refreshes data older than 10 minutes before a tool answers. After a failure it does not call the API for a minute and answers from the cache with a warning. When a cache already exists, auto-sync is capped at 30 seconds. The `sync` tool always calls the API.
- `recurring_payments` is a heuristic (same payee, similar amount, regular interval); its limits are noted in `internal/report/recurring.go`.

## Conventions

- Code style follows `~/Work/widgets-go`: clear names, small functions, errors wrapped with context (`fmt.Errorf("...: %w", err)`) and compared with `errors.Is`.
- Comments are in Russian, short (two lines at most) and explain *why*. No dead code and no commented-out code.
- README, tool descriptions and messages shown to the user are in Russian.
- The linter set is narrow on purpose (standard plus `bodyclose`, `errorlint`, `gocritic`, `misspell`, `nakedret`, `unconvert`, `wastedassign`, `govet.shadow`). Fix findings instead of adding `nolint`.
- Tests sit next to the code as `*_test.go`. The client is tested against `httptest`, the store against a real SQLite file in `t.TempDir()`, and tools through an in-memory MCP transport. Tests must not touch the real API.
- Configuration is env only, and every variable is documented in `.env.example`. `.env` is never committed.
