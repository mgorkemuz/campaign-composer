# Decisions

Key choices and why. Entries marked *proposed* are still open.

## Stack

| Area | Choice | Why |
| --- | --- | --- |
| Backend | Go, stdlib `net/http` router (method + path patterns) | No framework needed; fewer dependencies to explain. |
| Database | PostgreSQL 17 via `pgx/v5`, hand-written SQL, embedded `.sql` migrations | Segment rules compile to parameterised SQL, so we want direct control of the queries. |
| Local/test DB | `embedded-postgres` when `DATABASE_URL` is unset; one migrated DB per test | `make test` runs with no Docker and no shared state between tests. |
| Queue | Postgres table queue (`FOR UPDATE SKIP LOCKED` + leases) | Delivery rows are created in the same transaction as campaign approval, and a `UNIQUE (campaign_id, user_id, channel)` key makes double sends impossible at the DB level. No second datastore. Alternative considered: asynq + Redis (built-in retries and task-ID dedup, but a dual write between Postgres and Redis). |
| Rate limiting | `golang.org/x/time/rate`, one limiter per channel (100/s) | Single worker process, so an in-process token bucket is enough. |
| Realtime | Server-Sent Events, Postgres `LISTEN/NOTIFY` as the wake-up signal | One-way server → browser is all we need; works through plain HTTP. |
| LLM | Claude via the Messages API behind a small `Provider` interface; scripted fake for tests | Tests never touch the network; the real adapter is swapped in when `ANTHROPIC_API_KEY` is set. |
| LLM output | Forced tool use for JSON, then JSON Schema validation + a field/operator allowlist | The model never writes SQL; the backend owns query construction. |
| Metrics | `prometheus/client_golang` on `/metrics`, plus `/healthz` and `/readyz` | Standard, and the dashboard can read the same counters. |
| Frontend | Vue 3 + Vite + TypeScript, Tailwind v4 | Required Vue; Tailwind for speed. |
| Logging | `log/slog`, text locally, JSON in Compose | Stdlib, structured. |

## Reused from my earlier projects

The infrastructure was ported from my own projects and adapted:

- **Database layer:** `pgx` pool, embedded Postgres fallback, migrations, one database per test.
- **HTTP helpers and config:** strict JSON decoding, JSON errors, access logs, panic recovery, and a `.env`
  allowlist.
- **JSON Schema validator.**
- **Event hub and SSE stream:** generalised from "agent runs" to any topic.
- **Lease-based queue pattern.**
- **LLM interface and retry:** planned next.

What changed in the port, and why:

- **Queue leases have no heartbeat.** The original ran long agent jobs that needed one. Here each job is one
  provider call, so the handler's context ends before the lease does (lease 30 s). A crashed worker's job is
  requeued by the sweeper. The provider idempotency key covers the one gap left: the provider accepted a message,
  but the worker died before saving that.
- **One generic `jobs` table instead of a per-domain queue.** Sending, content generation and scheduled sends all
  use it; `run_at` doubles as the scheduling field.
- **Embedded Postgres binaries are unpacked once per machine** for tests, in the user cache folder, under a file
  lock. Unpacking took about 60 s per test package under `-race`; now the whole suite runs in seconds.

## Mock data

- **Seed:** 5,000 users from `internal/seed`, deterministic (fixed seed). The server inserts them on start if
  `users` is empty; `SEED_USERS` changes the count.
- **Fake by design:** names come from fixed lists. Emails use the reserved `example.*` domains and phones the
  Turkish mobile format. About 0.5% of phones and emails are malformed on purpose, so the mock provider's
  permanent-failure path has real cases.
- **No event-history table, to keep scope small:** each user has `last_cart_at`, `last_purchase_at`,
  `order_count` and `total_spent`.
- **Cart ages are skewed towards recent days**, as real activity is. The example brief ("İstanbul, cart in the
  last 30 days, no purchase") matches 322 users, and a test keeps it in a useful range.
- **Cities use their Turkish spelling** (`İstanbul`). The segment engine must match "Istanbul" too.

## Models

`claude-haiku-5-5` is the default: the tasks are short and structured, and manual testing stays at about
$0.001 per campaign. `ANTHROPIC_MODEL` switches it. Structured outputs can't express length limits, so the
backend validates SMS (≤ 160) and subject (≤ 80) itself.

## AI session exports

Raw app exports include the author's email, local paths, other projects' folder names and full subagent
transcripts. `tools/sessionexport` keeps only the main conversation and scrubs it. Private terms are never
hard-coded in the repo: the home dir and username come from the machine, and extra terms come from a
gitignored rules file. It fails closed: if a scan of the output still finds a credential pattern or private
term, nothing is written.
