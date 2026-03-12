CREATE TABLE IF NOT EXISTS schedules (
    id          SERIAL PRIMARY KEY,
    day_of_week VARCHAR(20) NOT NULL,
    week_type   VARCHAR(20) NOT NULL,
    start_time  TIME NOT NULL,
    end_time    TIME NOT NULL,
    thread_id   BIGINT NOT NULL

    notified_5min   BOOLEAN DEFAULT FALSE,
    notified_1min   BOOLEAN DEFAULT FALSE,
    notified_open   BOOLEAN DEFAULT FALSE,

    queue_message_id BIGINT DEFAULT NULL
);