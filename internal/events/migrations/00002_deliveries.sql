-- +goose Up
CREATE TABLE deliveries (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    source TEXT NOT NULL,
    id TEXT NOT NULL,
    record BLOB NOT NULL,
    outcome TEXT NOT NULL,
    UNIQUE(source, id)
);

-- +goose Down
DROP TABLE deliveries;
