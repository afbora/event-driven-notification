-- 000002_drop_updated_at_trigger.up.sql
--
-- Drop the BEFORE UPDATE triggers that overwrote updated_at with NOW().
--
-- The domain entity already manages timestamps through an injected clock
-- (CLAUDE.md §3.6); having Postgres clobber updated_at with NOW() on every
-- update breaks deterministic integration tests and means the application's
-- timestamp gets discarded silently. Application code is the source of
-- truth for updated_at from this migration onward; the trigger function
-- itself stays in place so future tables can opt in if they want it.

BEGIN;

DROP TRIGGER IF EXISTS set_updated_at_notifications ON notifications;
DROP TRIGGER IF EXISTS set_updated_at_templates    ON templates;

COMMIT;
