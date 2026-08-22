-- +goose Up
CREATE TABLE autoscale_policies (
    deployment TEXT PRIMARY KEY,
    policy_json TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE autoscale_states (
    deployment TEXT PRIMARY KEY,
    state_json TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE autoscale_states;
DROP TABLE autoscale_policies;
