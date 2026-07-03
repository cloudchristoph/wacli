CREATE TABLE messages_fts (
    rowid INTEGER PRIMARY KEY,
    text TEXT,
    media_caption TEXT,
    filename TEXT,
    chat_name TEXT,
    sender_name TEXT,
    display_text TEXT
);
