-- Tracks per-miner alert de-duplication state, kept in its own table rather
-- than added as columns on `samples` or `miners`: this SQLite build has no
-- "ALTER TABLE ... ADD COLUMN IF NOT EXISTS" (confirmed empirically against
-- modernc.org/sqlite - the syntax errors), so a plain ALTER TABLE here would
-- crash-loop the app on its second-ever startup once the column already
-- exists. A new table needs only CREATE TABLE IF NOT EXISTS, which is
-- already safe to re-run every startup.
CREATE TABLE IF NOT EXISTS alert_state (
    miner_id          INTEGER PRIMARY KEY REFERENCES miners(id) ON DELETE CASCADE,
    last_blocks_found INTEGER NOT NULL DEFAULT 0,
    high_diff_alerted INTEGER NOT NULL DEFAULT 0,
    updated_at        TEXT
);
