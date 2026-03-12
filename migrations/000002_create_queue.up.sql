CREATE TABLE IF NOT EXISTS queue_entries (
    id          SERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    username    VARCHAR(255),
    schedule_id INT NOT NULL REFERENCES schedules(id),
    position    INT NOT NULL,
    UNIQUE (schedule_id, position)
);