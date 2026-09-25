CREATE TABLE IF NOT EXISTS trip_chat_messages (
    id UUID PRIMARY KEY,
    trip_id UUID NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    sender_id UUID NOT NULL REFERENCES users(id),
    recipient_id UUID NOT NULL REFERENCES users(id),
    client_message_id UUID NOT NULL,
    body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    read_at TIMESTAMPTZ,
    CHECK (sender_id <> recipient_id),
    UNIQUE (trip_id, sender_id, client_message_id)
);

CREATE INDEX IF NOT EXISTS trip_chat_history_idx
    ON trip_chat_messages (trip_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS trip_chat_unread_idx
    ON trip_chat_messages (trip_id, recipient_id, created_at DESC)
    WHERE read_at IS NULL;
