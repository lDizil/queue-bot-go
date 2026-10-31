DROP TABLE runtime_settings;
DROP INDEX IF EXISTS queue_entries_schedule_user_unique;
DROP INDEX schedules_active_topic_unique;
ALTER TABLE queue_entries DROP COLUMN completed, DROP COLUMN display_name;
ALTER TABLE schedules
    DROP COLUMN chat_id, DROP COLUMN title,
    DROP COLUMN active_opens_at, DROP COLUMN active_closes_at,
    DROP COLUMN active_thread_id, DROP COLUMN active_capacity, DROP COLUMN active_columns;
