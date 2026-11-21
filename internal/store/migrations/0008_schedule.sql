-- Scheduled sending: approved now, sent later.
ALTER TABLE campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_status_check
    CHECK (status IN ('draft', 'audience_ready', 'content_ready', 'approved', 'scheduled',
                      'sending', 'completed', 'failed'));
ALTER TABLE campaigns ADD COLUMN scheduled_at timestamptz;
