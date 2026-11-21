-- One row per recipient and channel. The unique key makes a double send impossible
-- at the database level: approving twice, or a retried approval, inserts nothing new.
CREATE TABLE deliveries (
    id                  bigserial   PRIMARY KEY,
    campaign_id         uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    user_id             bigint      NOT NULL REFERENCES users (id),
    channel             text        NOT NULL CHECK (channel IN ('sms', 'email')),
    status              text        NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued', 'sending', 'retrying', 'sent',
                                          'delivered', 'undelivered', 'failed')),
    attempts            int         NOT NULL DEFAULT 0,
    provider_message_id text        UNIQUE,
    last_error          text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    sent_at             timestamptz,
    receipt_at          timestamptz,
    UNIQUE (campaign_id, user_id, channel)
);

-- Counting a campaign's deliveries by status drives the live dashboard and the
-- completion check.
CREATE INDEX deliveries_campaign_status_idx ON deliveries (campaign_id, status);

ALTER TABLE campaigns
    ADD COLUMN approved_at  timestamptz,
    ADD COLUMN completed_at timestamptz;
