-- Resending messages that did not arrive.
ALTER TABLE deliveries
    -- Round of sending: 0 for the first, +1 per resend. The provider reference and
    -- idempotency key include it, so each round is sent exactly once and a late
    -- receipt from an earlier round cannot update a newer one.
    ADD COLUMN resend_count int     NOT NULL DEFAULT 0,
    -- The provider rejected the message for good (e.g. a malformed number):
    -- resending would fail the same way, so resend skips it.
    ADD COLUMN permanent    boolean NOT NULL DEFAULT false;
