CREATE TABLE global_admins (
    user_id BIGINT PRIMARY KEY CHECK (user_id > 0)
);

CREATE TABLE admin_permissions (
    user_id BIGINT NOT NULL REFERENCES global_admins (user_id) ON DELETE CASCADE,
    permission TEXT NOT NULL CHECK (permission IN (
        'view_chat_id', 'create_one_shot', 'manage_queue_messages', 'edit_schedule',
        'manage_chats', 'change_week', 'mark_others', 'remove_participants'
    )),
    PRIMARY KEY (user_id, permission)
);

CREATE TABLE access_bootstrap (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton)
);
