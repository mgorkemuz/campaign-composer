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

## Segment rules

- **One field catalog drives everything** (`internal/segment/fields.go`): the JSON schema Claude must follow,
  validation, the SQL column for each field, and the UI's field list (`GET /api/segments/fields`). The schema
  is generated from the catalog, so they cannot drift apart.
- **No text from a rule ever reaches SQL.** Columns and operators come from the catalog, and values are always
  `$n` parameters. Text values must be one of the catalog's known values, so an injection attempt fails as
  "unknown city" before compiling.
- **At most two levels** ("A and (B or C)"). Structured outputs don't allow recursive schemas, and deeper logic
  is hard for a marketer to read anyway.
- **Consent is not a rule field.** It's applied per channel at send time, so no rule can reach users who didn't
  consent.
- **Validation lists every problem with its path** (`conditions[1].value: …`), so one round trip to Claude
  can fix them all.
- **Strings are not accepted as numbers** (`"30"` is rejected), so mistakes surface instead of being guessed at.
- **"Within N days" is anchored to one `now`,** so a preview and the send that follows can use the same cut-off.
- **Tests compare results with hand-written SQL.** They also check that opposite operators split the users
  exactly (e.g. `within_days 45` + `not_within_days 45` = everyone, which catches a forgotten `NULL`), and
  that 200 random catalog rules all run.

## LLM integration

- **Official Anthropic Go SDK** behind a small `Provider` interface (ported from my earlier project). Every call
  streams.
- **JSON comes from structured outputs** (`output_config.format`), not a forced tool call. Newer models reject
  forced `tool_choice`, and structured outputs keep the answer's shape fixed.
- **The backend validates what the schema can't express:** known fields and values, number ranges, and later
  message lengths.
- **One retry policy.** The SDK's own retries are off, and ours retries 429/5xx/529 with backoff and jitter,
  honoring `Retry-After`. Errors after text has streamed are never retried, so an answer is never shown twice.
- **One repair round.** If the answer fails validation, Claude gets one fresh request with the rejected answer and
  every problem listed. A second failure is shown to the marketer, who can reword the brief or edit the rule by
  hand. The rejected answer is quoted in a new user message, not replayed as an assistant turn, which keeps
  requests valid under newer models' history checks.
- **Errors map to what the user can do:**

  | Code | Meaning |
  | --- | --- |
  | `llm_invalid_output` (422) | The answer was still invalid after the repair round. |
  | `llm_refused` (422) | The model declined the brief. |
  | `llm_unavailable` (502) | Temporary: try again. |
  | `llm_rejected` (502) | Configuration: bad key, missing workspace. |

  A real call found the last one: an organization key needs `ANTHROPIC_WORKSPACE_ID`, and the first version
  wrongly said "try again".
- **Without an API key, a keyword-based mock** answers in the same JSON shapes and goes through the same
  validation, so the app works offline. Tests use a separate scripted fake.
- **The brief sits in `<brief>` tags,** and the prompt treats it as a description of the campaign, not as
  instructions.

## Messages

- **One language per campaign**, `tr` or `en`. By default the model uses the brief's language. If the marketer
  picks one, the schema's `language` enum is narrowed to it, so the model cannot answer in another.
- **Limits are checked after personalization against this audience,** not the template alone. The backend reads
  the longest first name, last name and city among the matched users and renders the worst case. An SMS that fits
  for "Ankara" can be too long for "Eskişehir". Changing the audience re-checks existing messages: they become
  `invalid` (with the reason) or stay `ready`, without being rewritten.
- **SMS encoding is reported, not enforced.** The requirement is ≤ 160 characters. Turkish letters (ş, ğ, ı, İ)
  switch an SMS to UCS-2, where one part holds 70 characters, so a 149-character Turkish SMS goes out as 3 parts.
  The check shows encoding and part count so the marketer can decide.
- **Generation is a queue job, not part of the HTTP request.** The request answers 202 at once. Text streams to
  the campaign's SSE stream as field deltas (`{"attempt","field","text"}`), extracted from Claude's partial JSON
  by a streaming parser. A page refresh or server restart doesn't lose the result, and temporary LLM errors are
  retried by the queue.
- **Newest wins.** Every generation request or hand edit bumps `content_version`, and a job saves only if the
  version is still its own. While a generation runs, the audience is locked (409 `generating`).
- **Hand edits pass the same checks** as generated messages (`PUT /content`, 422 with every problem).
- **The database must be UTF-8.** Embedded Postgres defaulted to `SQL_ASCII`, where `char_length('Eskişehir')`
  is 10 (bytes), not 9. A test caught this through a wrong SMS length. Embedded databases are now created as
  UTF-8, the server refuses to start on any other encoding, and a test fails if a test database isn't UTF-8.

## Sending

- **Approval is one transaction.** It re-checks the messages against the audience as it is now ("within 30 days"
  shifts with time). It inserts every delivery with `INSERT … SELECT` (consent filter per channel) and every send
  job the same way, then moves the campaign to `sending`. It all commits together or not at all. A second approve
  answers 409 and inserts nothing.
- **Never twice: four independent layers.**
  1. `UNIQUE (campaign_id, user_id, channel)` on deliveries.
  2. One job per delivery under a unique dedup key.
  3. A worker skips a delivery that is already finished.
  4. An `Idempotency-Key` per delivery sent to the provider, which answers a repeat with its first result. Only
     this covers "the provider accepted the message but its response was lost".

  The chaos test proves it. A mutation that sends a fresh key per attempt makes it fail with deliveries accepted
  twice.
- **Retries.**
  - 429, 5xx and network errors are retried with exponential backoff and jitter (1 s base, 60 s cap), or after
    the provider's `Retry-After`, up to 6 attempts.
  - Other 4xx (invalid number or email) fail at once.
  - When the queue gives up, the delivery is marked `failed`.
- **Rate limit:** a token bucket per channel (`golang.org/x/time/rate`), 100/s with a burst of 1, so the limit
  also holds over short windows. Each channel has its own worker pool (40 workers, enough for ~100/s at the mock's
  50–500 ms latency), and every send waits for its channel's limiter. The limiter is per process: with several app
  replicas it would need to move to Postgres or Redis. A test checks that no one-second window exceeds the limit.
- **Receipts by signed webhook.**
  - The signature is HMAC-SHA256 over `timestamp.body`; requests older than 5 minutes are refused.
  - Receipts are matched by our delivery ID (the provider echoes it as `reference`), because a receipt can arrive
    before the worker has saved the provider's message ID.
  - Status only moves forward. Duplicate and late receipts change nothing.
  - A `delivered` receipt also overrides our own `failed`: we gave up, but the provider had accepted the message.
  - Unknown or duplicate receipts get 200, so the provider stops retrying; database errors get 500, so it retries.
- **Campaign completion:** `completed` once no delivery is queued, sending or retrying. Receipts may still arrive
  afterwards and update deliveries.
- **Live doorbells are throttled** to one per campaign every 250 ms, with a trailing ring, so thousands of status
  changes cost a few notifications a second.
- **Metrics on `/metrics`:**
  - provider calls by status code, with latency;
  - finished deliveries;
  - webhooks by outcome;
  - LLM calls and tokens by task;
  - queue depth, read from the jobs table at scrape time.

  The Docker run caught permanent failures being counted twice: both the handler and the queue's `OnFailed`
  "succeeded" in marking the same row. Now the second update skips rows already failed, and the chaos test compares
  the counter with the database.
- **Mock provider:** a separate service (`cmd/mockprovider`), with every misbehaviour set by an environment
  variable. In Docker, a 2,623-message campaign finished in about 24 s with 0 duplicates, and the 14 failures were
  exactly the malformed contacts.

## Models

`claude-haiku-5-5` is the default: the tasks are short and structured.

**Measured on real calls:** an audience generation takes about 1,850 input and 230–550 output tokens (about
$0.0003) and 2.5–4 s. Message generation takes about 800 input and 400–540 output tokens (about $0.0003); the
first text appears after 1.5–3.5 s, and it's done in 3–4 s. It produced the expected rule for the example brief and for a Turkish brief, and refine
edited the existing rule instead of rebuilding it. `ANTHROPIC_MODEL` switches it. Structured outputs can't express length limits, so the
backend validates SMS (≤ 160) and subject (≤ 80) itself.

## AI session exports

Raw app exports include the author's email, local paths, other projects' folder names and full subagent
transcripts. `tools/sessionexport` keeps only the main conversation and scrubs it. Private terms are never
hard-coded in the repo: the home dir and username come from the machine, and extra terms come from a
gitignored rules file. It fails closed: if a scan of the output still finds a credential pattern or private
term, nothing is written.
