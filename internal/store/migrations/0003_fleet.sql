CREATE TABLE IF NOT EXISTS fleet_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS credentials (
    miner_id INTEGER PRIMARY KEY REFERENCES miners(id) ON DELETE CASCADE,
    ciphertext BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    miner_id INTEGER NOT NULL REFERENCES miners(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    operation TEXT NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(miner_id, name)
);
CREATE TABLE IF NOT EXISTS schedules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    miner_id INTEGER NOT NULL REFERENCES miners(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    timezone TEXT NOT NULL,
    days TEXT NOT NULL,
    at TEXT NOT NULL,
    operation TEXT NOT NULL,
    profile_id INTEGER REFERENCES profiles(id) ON DELETE CASCADE,
    enabled INTEGER NOT NULL DEFAULT 0,
    last_run TEXT NOT NULL DEFAULT '',
    last_result TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    miner_id INTEGER REFERENCES miners(id) ON DELETE SET NULL,
    ts TEXT NOT NULL,
    category TEXT NOT NULL,
    operation TEXT NOT NULL,
    detail TEXT NOT NULL,
    success INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts);
CREATE TABLE IF NOT EXISTS health_state (
    miner_id INTEGER NOT NULL REFERENCES miners(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(miner_id, kind)
);
