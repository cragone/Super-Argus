-- +goose Up
CREATE TABLE notification_channels (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('webhook')),
    config TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE alert_rules (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    name TEXT NOT NULL,
    query TEXT NOT NULL,
    comparator TEXT NOT NULL CHECK (comparator IN ('gt','gte','lt','lte','eq')),
    threshold REAL NOT NULL DEFAULT 0,
    lookback TEXT NOT NULL DEFAULT '5m',
    for_duration TEXT NOT NULL DEFAULT '0s',
    cooldown TEXT NOT NULL DEFAULT '0s',
    severity TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('info','warning','critical')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    notify_channel_ids TEXT NOT NULL DEFAULT '[]',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE alert_states (
    rule_id TEXT PRIMARY KEY REFERENCES alert_rules (id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'ok' CHECK (state IN ('ok','pending','firing')),
    since TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_eval TIMESTAMP,
    last_value REAL NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    notified BOOLEAN NOT NULL DEFAULT FALSE,
    last_notified_at TIMESTAMP
);

CREATE TABLE alert_events (
    id TEXT PRIMARY KEY,
    rule_id TEXT NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('pending','firing','resolved')),
    value REAL NOT NULL DEFAULT 0,
    at TIMESTAMP NOT NULL,
    notified BOOLEAN NOT NULL DEFAULT FALSE,
    notify_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_alert_events_rule ON alert_events (rule_id, at);
CREATE INDEX idx_alert_events_at ON alert_events (at);

-- +goose Down
DROP TABLE alert_events;
DROP TABLE alert_states;
DROP TABLE alert_rules;
DROP TABLE notification_channels;
