-- Per-user language: the campaign's messages adapted into each other language its
-- audience speaks. Users get their language's version; anyone else gets the
-- campaign's main language.
CREATE TABLE campaign_translations (
    campaign_id   uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    language      text        NOT NULL,
    -- campaigns.content_version this was made from: a translation of older
    -- messages is never used.
    version       int         NOT NULL,
    state         text        NOT NULL CHECK (state IN ('translating', 'ready', 'failed')),
    sms           text        NOT NULL DEFAULT '',
    email_subject text        NOT NULL DEFAULT '',
    email_body    text        NOT NULL DEFAULT '',
    content_check jsonb,
    error         jsonb,
    recipients    int         NOT NULL DEFAULT 0, -- reachable users who speak it
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (campaign_id, language)
);
