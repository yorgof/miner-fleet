CREATE TABLE IF NOT EXISTS miners (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL, -- 'axeos' or 'avalon_cgminer'
    host       TEXT NOT NULL,
    port       INTEGER NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS samples (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    miner_id         INTEGER NOT NULL REFERENCES miners(id) ON DELETE CASCADE,
    ts               TEXT NOT NULL,
    ok               INTEGER NOT NULL DEFAULT 1,
    error            TEXT,
    hashrate_ghs     REAL,
    temp_c           REAL,
    vr_temp_c        REAL,
    power_w          REAL,
    voltage_mv       REAL,
    fan_rpm          INTEGER,
    fan_percent      REAL,
    auto_fan_mode    INTEGER,
    shares_accepted  INTEGER,
    shares_rejected  INTEGER,
    best_diff        REAL,
    best_session_diff REAL,
    uptime_s         INTEGER,
    wifi_rssi        INTEGER,
    raw_json         TEXT
);

CREATE INDEX IF NOT EXISTS idx_samples_miner_ts ON samples(miner_id, ts);
