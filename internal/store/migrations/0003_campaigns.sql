-- One campaign per brief. Message content and sending columns arrive with their phases.
CREATE TABLE campaigns (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    brief                text        NOT NULL CHECK (length(brief) BETWEEN 1 AND 2000),
    status               text        NOT NULL DEFAULT 'draft'
                         CHECK (status IN ('draft', 'audience_ready', 'content_ready',
                                           'approved', 'sending', 'completed', 'failed')),
    rule                 jsonb,
    audience_summary     text        NOT NULL DEFAULT '',
    audience_assumptions jsonb       NOT NULL DEFAULT '[]',
    audience_total       int,
    sms_reachable        int,
    email_reachable      int,
    llm_model            text        NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX campaigns_created_idx ON campaigns (created_at DESC);
