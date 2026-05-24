-- 000001_initial_schema.down.sql
--
-- Reverses 000001_initial_schema.up.sql. Tables are dropped in reverse FK
-- order; the trigger function is dropped last because it is referenced by
-- every triggered table.

BEGIN;

DROP TABLE IF EXISTS notification_logs;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS templates;
DROP TABLE IF EXISTS batches;

DROP FUNCTION IF EXISTS trigger_set_updated_at();

COMMIT;
