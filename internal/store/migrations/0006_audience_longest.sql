-- The longest first name, last name and city in the current audience, saved with
-- the audience so the UI can count SMS length live while the marketer types.
ALTER TABLE campaigns ADD COLUMN audience_longest jsonb;
