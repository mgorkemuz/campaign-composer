# Smart Campaign Composer

A marketer describes a campaign in plain language. Claude turns it into a validated audience segment and
SMS/email copy. The marketer reviews and approves, and the system sends through a rate-limited queue to a mock
provider while a dashboard shows delivery status live.

> Status: the core flow is complete: brief → AI audience → AI messages → approval → sending → live dashboard.
> Remaining: architecture write-up and session exports (phase 8), then bonus features.
> See [docs/PLAN.md](docs/PLAN.md) for the phases.

## Quick start

```bash
cp .env.example .env   # optional: add ANTHROPIC_API_KEY, otherwise the mock LLM is used
docker compose up
```

Open http://localhost:8080.

| Command      | What it does                                  |
| ------------ | --------------------------------------------- |
| `make up`    | Build and start everything with Docker Compose |
| `make run`   | Run the API locally (serves `web/dist` if built) |
| `make web`   | Frontend dev server with hot reload on :5173 (proxies the API on :8080) |
| `make test`  | Run all Go and frontend tests (no API key, Docker or network needed) |
| `make session IN=<zip> NAME=<name>` | Scrub a Claude session export into `ai-sessions/` ([how](ai-sessions/README.md)) |

### Services

| Service | Port | Notes |
| --- | --- | --- |
| `server` | 8080 | Web app, API, workers, `/healthz`, `/readyz`, `/metrics` |
| `mockprovider` | 9090 | Fake SMS/email provider; `GET /stats` shows what it saw |
| `postgres` | 55432 | `composer` / `composer` (local only) |

### API

| Endpoint | Purpose |
| --- | --- |
| `POST /api/campaigns` | Create a campaign from a brief |
| `GET /api/campaigns`, `GET /api/campaigns/{id}` | List / read campaigns |
| `POST /api/campaigns/{id}/audience` | Generate the audience rule with the LLM |
| `POST /api/campaigns/{id}/audience/refine` | Change the rule with an instruction |
| `PUT /api/campaigns/{id}/audience` | Save a hand-edited rule |
| `POST /api/campaigns/{id}/content` | Generate messages (streams to `/events`) |
| `PUT /api/campaigns/{id}/content` | Save hand-edited messages |
| `POST /api/campaigns/{id}/approve` | Queue one delivery per consenting recipient and channel |
| `GET /api/campaigns/{id}/events` | Server-sent events: status changes, message text as it is written, delivery progress (counts, throughput, pixel-grid data) |
| `GET /api/campaigns/{id}/deliveries[/counts]` | Delivery list (filter by `status`) and counts |
| `GET /api/segments/fields`, `POST /api/segments/preview` | Rule fields; preview counts for a rule |
| `POST /webhooks/provider` | Signed delivery receipts from the provider |

## Architecture

To be written in full at the end. See [DECISIONS.md](DECISIONS.md) for the stack and the reasons behind it.

## Not done yet

- Architecture write-up and session exports (phase 8)
- High scale: built for thousands of users. What would break first at a million, with measurements and the fix
  for each, is in [DECISIONS.md › Known limits at high scale](DECISIONS.md#known-limits-at-high-scale).
