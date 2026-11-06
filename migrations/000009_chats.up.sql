CREATE TABLE chats (
    chat_id BIGINT PRIMARY KEY CHECK (chat_id <> 0),
    name TEXT NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE UNIQUE INDEX chats_single_default ON chats (is_default) WHERE is_default;

INSERT INTO chats (chat_id)
SELECT DISTINCT chat_id FROM schedules WHERE chat_id <> 0;
