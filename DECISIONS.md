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
- **LLM interface and retry:** a small `Provider` interface over the SDK, with retry on 429/5xx and `Retry-After`.

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
- **The backend validates what the schema can't express:** known fields and values, number ranges, and message
  lengths.
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

- **The campaign is written in one language**, `tr` or `en`. By default the model uses the brief's language. If
  the marketer picks one, the schema's `language` enum is narrowed to it, so the model cannot answer in another.
  Users who speak the other language get a translation (see [Bonus features](#bonus-features)).
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
- **The copy is written for this audience, not just from the brief.** The message step gets the audience's
  conditions in plain words (`segment.Describe`, e.g. "Last purchase: not in the last 60 days (or never)"). The
  prompt asks Claude to work out the goal: win back lapsed buyers, get carts finished, welcome new sign-ups. It then
  says why the person is hearing from you, gives one concrete reason to act, and ends with a clear call to action.
  This step runs at medium effort (the audience step stays low). The first version only passed a one-line summary,
  and the copy came out generic.
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

## Live delivery progress

- **Progress is computed, not stored per change.** Workers only ring a throttled doorbell (≤ 4/s per campaign).
  The stream then sends a `progress` snapshot at most every 500 ms, with a trailing send so the final state always
  arrives. 10,000 status changes therefore cost about two small queries a second per viewer, not 10,000 events.
- **One query builds the snapshot:**
  - counts per channel and status;
  - throughput over the last 5 s;
  - retries;
  - receipts still pending;
  - a **grid string**: one character per delivery in delivery order (`q s r t d u f`). The dashboard's pixel
    grid draws one square per character. About 2,600 deliveries make a ~2.6 KB string, small enough to resend
    whole each time instead of diffing.
- **Stored events and snapshots share one SSE stream.** A reconnecting browser replays missed stored events from
  `Last-Event-ID` and gets a fresh snapshot at once.
- **The stream ends only when nothing more will change:** sending finished and every receipt in, with a 2-minute
  grace period for receipts that never come. The "is it finished" check reads the cheap campaign status first and
  builds the full snapshot only after sending is done.
- **In Docker,** a 2,623-message campaign streamed 46 snapshots over 24 s, about 90 msg/s per channel at the
  100/s limit, and the stream closed itself once the last receipt arrived.

## Frontend

- **Stack:**
  - Vue 3 (`<script setup>`, TypeScript), vue-router, Vite and Tailwind v4.
  - Design tokens from my earlier project (shadcn neutral with a warm tint, Geist font, 0.625rem radius).
  - Small hand-written components instead of the shadcn-vue CLI, to keep dependencies few.
  - Fonts are self-hosted through `@fontsource`.
- **One origin.** The Go server serves the built app (`WEB_DIR`, with an `index.html` fallback for client
  routes), so no CORS is needed. In development, Vite proxies `/api` to the server.
- **Live updates through `EventSource`,** which reconnects by itself with `Last-Event-ID`. Message text streams
  into the editors and previews as `delta` events. A new attempt number (the repair round) clears the boxes and
  starts over.
- **The pixel grid is a `<canvas>`,** one square per delivery coloured by status, drawn from the `progress`
  event's grid string. Thousands of squares redrawn twice a second would be slow as DOM elements. Hovering a square
  shows its status. The status colours come from CSS variables shared with the badges and bars.
- **The SMS counter mirrors the server's check in TypeScript** (worst-case length against the audience's longest
  names, GSM-7 vs UCS-2 parts), so it updates as the marketer types. The server still validates on save and at
  approval. The audience's longest values are saved with the audience (`audience_longest`), so both sides measure
  the same thing.
- **Rule chips:** edit a value in place, remove a condition, or "refine" with an instruction to Claude. Every edit
  goes through the same server validation, and errors come back with the full list of problems.
- **One decision: send.** The audience is already in the brief, so asking the marketer to approve it again added a
  click and no information. After the brief, the app builds the audience and writes the messages without stopping.
  The marketer lands on a single "Ready to send" page with Edit buttons, and "Send to N" is the only approval: one
  click. The button carries the exact count, so a second "Are you sure?" step added a click and no information.
- **Screens:** Brief → Preparing (automatic, shows each step and the SMS streaming in) → Ready to send → Live. A
  small step indicator runs across the top, with as little text as possible; editing is behind "Edit".
- **The server chains the steps, not the browser.** When the AI finishes an audience and the campaign has no
  messages yet, the backend queues message generation itself. So a campaign reaches "ready to send" even if the tab
  is closed, or is an old copy of the app that doesn't know the new flow. (The first version chained in the browser.
  A stale tab still showed the old "Write messages" button and left campaigns half done.) Hand-edited rules don't
  trigger it: by then the marketer is on the review page.
- **Home page hero,** inspired by pixel-grid web design: a large two-line headline and a justified tagline over a
  vertical rule. Below is a full-width band of small squares in the delivery-status colours, generated from value
  noise, dense at the top and dissolving downwards. It drifts slowly; it doesn't animate when reduced motion is set
  or the tab is hidden. All wording and graphics are our own. The background grid is a fine 12 px.
- **Checked in the browser:** a Turkish brief went through audience (auto-started), streamed messages, approval
  and the live dashboard, at desktop and phone width, with no console errors. That walk-through caught auto-start
  silently doing nothing: the step's component wasn't rendered yet when it was called. It's fixed with `nextTick`.

## Bonus features

- **CI** (`.github/workflows/ci.yml`): on every push and pull request, GitHub runs the Go tests (with `-race`,
  `go vet` and `gofmt`), the frontend type check, tests and build, and a Docker image build. It needs no secrets:
  tests use the mock LLM and embedded Postgres, whose binaries are cached between runs.
- **Scheduled sending.** "Schedule" on the review page approves now and sends later.
  - The audience is fixed at approval (the people the marketer reviewed), and each send job's `run_at` is the
    chosen time, so the queue's scheduling does the waiting.
  - The campaign shows `scheduled` until its first message goes out, and can be cancelled until then: its queued
    jobs and deliveries are removed and it returns to "ready to send".
  - The time must be 1 minute to 30 days ahead, checked against real time. A test caught the check using the
    pinned business clock, which accepted a time in the past.
- **Each user in their own language.** Once the main messages are ready (generated or edited), the server looks at
  which languages the reachable audience speaks and queues **one translation job per other language**. It is never
  one per user: messages are templates, so 100 languages cost at most 100 short calls, whatever the audience size.
  - Claude adapts rather than translates word for word.
  - The result must keep exactly the same `{{variables}}` per field, and pass the same length checks measured for
    that language's users (one repair round, like the main messages).
  - Each translation records the `content_version` it was made from. Users get their language's ready translation
    of the current messages, and otherwise the main language: failed, outdated or missing translations all fall
    back.
  - Approval waits while translations are being written. The translation workers' concurrency bounds parallel LLM
    calls, which keeps rate limits in check with many languages.
  - Tests check each user receives their own language's SMS, and the fallback for failed and outdated
    translations.
- **A/B variants: left out on purpose.** The mock provider reports delivery only: no opens, clicks or purchases.
  Comparing two versions would only compare delivery rates, which say nothing about which copy works better.
  With engagement events, it would be a variant column on deliveries plus a split at approval.

## Known limits at high scale

Built and tested for thousands of users, as the case asks. I measured what changes at **1,000,000 deliveries in
one campaign** (one throwaway Postgres database, Docker on a laptop). At the required 100 msg/s per channel, such
a campaign sends for about 83 minutes per channel, so every screen and query must stay cheap that whole time.
In the order they would break:

| What breaks | Measured at 1M | Why | Fix when needed |
| --- | --- | --- | --- |
| **Live dashboard** | progress snapshot 3.8 s, 1 MB grid string; counts alone 115 ms | Each viewer's stream re-aggregates every delivery every 500 ms, and the grid has one character per message | A fixed-size grid (~2,000 squares, each a bucket of messages coloured by its worst status). Counts in a counters table, or computed once per campaign per second and shared by all viewers |
| **Approval** | 15.6 s for 1M deliveries + 1M jobs in one transaction | One `INSERT … SELECT` per table inside the HTTP request | Answer at once and fan out in background batches of ~10,000; the unique key already makes a repeated batch harmless |
| **Storage and write load** | 296 MB deliveries + 268 MB jobs per 1M messages; ~4 row updates per message | A job row per delivery, and finished jobs are never deleted | Make the delivery row the queue (lease columns on `deliveries`), claim in small batches, delete finished jobs after N days, partition deliveries by month |
| **Rate limit with replicas** | n/a | The token bucket lives in one process; two app replicas would send 200/s per channel | A shared limiter in Postgres or Redis, or one elected sender per channel |
| **Provider outage** | n/a | A million jobs would cycle through retries (spread by backoff and jitter, but still churn) | A circuit breaker per channel that pauses sending while the provider's error rate is high |
| **Audience preview** | n/a (~100–300 ms expected without indexes) | Full scans over `users` for each preview and the longest-name check | Indexes on the filterable columns; cache a rule's preview for a few seconds |
| **"Not delivered" list** | n/a | Sorted by `updated_at` within a campaign | An index on `(campaign_id, status, updated_at)` |

Already fine at that size:
- A queue claim stays at 10 ms with 400,000 jobs waiting (partial index plus `SKIP LOCKED`).
- Doorbells are throttled.
- The LLM runs once per campaign, not per person.
- The exactly-once guarantees don't depend on size.
- The home page band reads only the newest rows by primary key.

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
