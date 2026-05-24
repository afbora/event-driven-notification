-- 000002_drop_updated_at_trigger.down.sql
--
-- Re-attach the BEFORE UPDATE triggers that 000002 dropped.

BEGIN;

CREATE TRIGGER set_updated_at_notifications
    BEFORE UPDATE ON notifications
    FOR EACH ROW
    EXECUTE FUNCTION trigger_set_updated_at();

CREATE TRIGGER set_updated_at_templates
    BEFORE UPDATE ON templates
    FOR EACH ROW
    EXECUTE FUNCTION trigger_set_updated_at();

COMMIT;
