# Smart Campaign Composer

A marketer describes a campaign in plain language. Claude turns it into a validated audience segment and
SMS/email copy. The marketer reviews and approves, and the system sends through a rate-limited queue to a mock
provider while a dashboard shows delivery status live.

> Status: Phases 0–3 done (infrastructure, mock data, segment engine, LLM audience step). See [docs/PLAN.md](docs/PLAN.md) for the phases.

## Quick start

```bash
cp .env.example .env   # optional: add ANTHROPIC_API_KEY, otherwise the mock LLM is used
docker compose up
```

| Command      | What it does                                  |
| ------------ | --------------------------------------------- |
| `make up`    | Build and start everything with Docker Compose |
| `make run`   | Run the API locally                            |
| `make test`  | Run all tests (no API key needed)              |
| `make session IN=<zip> NAME=<name>` | Scrub a Claude session export into `ai-sessions/` ([how](ai-sessions/README.md)) |

## Architecture

To be written as the components land. See [DECISIONS.md](DECISIONS.md) for the stack and the reasons behind it.

## Not done yet

Everything except the scaffold.
