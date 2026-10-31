ALTER TABLE schedules
    ADD COLUMN chat_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN title TEXT NOT NULL DEFAULT '',
    ADD COLUMN active_opens_at TIMESTAMPTZ,
    ADD COLUMN active_closes_at TIMESTAMPTZ,
    ADD COLUMN active_thread_id BIGINT,
    ADD COLUMN active_capacity INT NOT NULL DEFAULT 0,
    ADD COLUMN active_columns INT NOT NULL DEFAULT 0;

ALTER TABLE queue_entries
    ADD COLUMN completed BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX schedules_active_topic_unique
    ON schedules (chat_id, active_thread_id)
    WHERE active_opens_at IS NOT NULL;

DO $$
BEGIN
    LOCK TABLE queue_entries IN SHARE ROW EXCLUSIVE MODE;
    IF NOT EXISTS (
        SELECT 1 FROM queue_entries GROUP BY schedule_id, user_id HAVING COUNT(*) > 1
    ) THEN
        CREATE UNIQUE INDEX queue_entries_schedule_user_unique ON queue_entries (schedule_id, user_id);
    END IF;
END $$;

CREATE TABLE runtime_settings (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    value JSONB NOT NULL
);
