CREATE TABLE menu_cleanup (
    generation TEXT PRIMARY KEY,
    owner_id BIGINT NOT NULL,
    chat_id BIGINT NOT NULL,
    thread_id INTEGER NOT NULL DEFAULT 0,
    message_id INTEGER NOT NULL DEFAULT 0,
    ephemeral_id INTEGER NOT NULL DEFAULT 0,
    deadline TIMESTAMPTZ NOT NULL,
    CONSTRAINT menu_cleanup_transport CHECK (
        (message_id > 0 AND ephemeral_id = 0) OR
        (message_id = 0 AND ephemeral_id > 0)
    ),
    UNIQUE (owner_id, chat_id, message_id, ephemeral_id)
);
