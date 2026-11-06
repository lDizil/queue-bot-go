CREATE TABLE queue_rejoin_blocks (
    id BIGSERIAL PRIMARY KEY,
    schedule_id INT NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
    active_opens_at TIMESTAMPTZ NOT NULL,
    user_id BIGINT NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    blocked_until TIMESTAMPTZ NOT NULL,
    UNIQUE (schedule_id, active_opens_at, user_id)
);
