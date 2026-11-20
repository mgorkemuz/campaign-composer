-- Infrastructure tables: the job queue and the event log. Domain tables come in later migrations.

-- Job queue. Workers claim rows with FOR UPDATE SKIP LOCKED and hold them under a
-- lease; a sweeper requeues rows whose lease expired (the worker died).
CREATE TABLE jobs (
    id               bigserial PRIMARY KEY,
    kind             text        NOT NULL,
    -- Idempotency: enqueueing the same key twice keeps the first job only.
    dedup_key        text        UNIQUE,
    payload          jsonb       NOT NULL DEFAULT '{}',
    status           text        NOT NULL DEFAULT 'queued'
                     CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    attempts         int         NOT NULL DEFAULT 0,
    max_attempts     int         NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    run_at           timestamptz NOT NULL DEFAULT now(),
    lease_owner      text,
    lease_expires_at timestamptz,
    last_error       text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz
);

CREATE INDEX jobs_ready_idx ON jobs (kind, run_at, id) WHERE status = 'queued';
CREATE INDEX jobs_lease_idx ON jobs (lease_expires_at) WHERE status = 'running';

-- Event log, one ordered stream per topic (e.g. a campaign). Live streams replay
-- from it after Last-Event-ID; NOTIFY is only a doorbell.
CREATE TABLE event_topics (
    topic    text   PRIMARY KEY,
    last_seq bigint NOT NULL DEFAULT 0
);

CREATE TABLE events (
    topic      text        NOT NULL REFERENCES event_topics (topic) ON DELETE CASCADE,
    seq        bigint      NOT NULL,
    type       text        NOT NULL,
    data       jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (topic, seq)
);
