-- +goose Up
CREATE TABLE IF NOT EXISTS peer_policies (
    peer_name TEXT PRIMARY KEY,
    grants_json TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS peer_policies;
