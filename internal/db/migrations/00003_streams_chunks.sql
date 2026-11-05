-- +goose Up
CREATE TABLE streams (
    id BIGINT PRIMARY KEY,
    labels TEXT NOT NULL,
    labels_text TEXT NOT NULL UNIQUE,
    api_key_id TEXT NOT NULL DEFAULT '',
    first_seen TIMESTAMP NOT NULL,
    last_seen TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_streams_api_key ON streams (api_key_id);
CREATE INDEX idx_streams_last_seen ON streams (last_seen);

CREATE TABLE stream_labels (
    stream_id BIGINT NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (stream_id, key)
);
CREATE INDEX idx_stream_labels_kv ON stream_labels (key, value);

CREATE TABLE chunks (
    id TEXT PRIMARY KEY,
    stream_id BIGINT NOT NULL REFERENCES streams (id),
    path TEXT NOT NULL,
    min_ts BIGINT NOT NULL,
    max_ts BIGINT NOT NULL,
    line_count INTEGER NOT NULL,
    bytes_compressed BIGINT NOT NULL,
    bytes_raw BIGINT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_chunks_stream_time ON chunks (stream_id, min_ts, max_ts);
CREATE INDEX idx_chunks_max_ts ON chunks (max_ts);

-- +goose Down
DROP TABLE chunks;
DROP TABLE stream_labels;
DROP TABLE streams;
