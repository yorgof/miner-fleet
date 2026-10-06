// Package store owns the SQLite schema and all queries. No ORM: the query
// set is small and fixed, and hand-written SQL is easier to reason about
// than a generic mapping layer for a dozen queries.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	key  []byte
	path string
}

// migrationsFS holds every *.sql file in migrations/, applied in filename
// order at every startup. Each file must be safe to re-run against a
// database that already has it applied (CREATE TABLE IF NOT EXISTS, ALTER
// TABLE ... ADD COLUMN IF NOT EXISTS) - there is no applied-migrations
// tracking table, deliberately, since every statement is already idempotent
// and a tracking table would just be a second thing that can drift from
// reality.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // modernc.org/sqlite + WAL: one writer, avoids SQLITE_BUSY under our low write volume
	if err := applyMigrations(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := os.Stat(path + ".key"); errors.Is(err, os.ErrNotExist) {
		var encrypted int
		if err := db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM credentials)+(SELECT count(*) FROM profiles)").Scan(&encrypted); err != nil {
			db.Close()
			return nil, err
		}
		if encrypted > 0 {
			db.Close()
			return nil, fmt.Errorf("restore the matching .db.key file before opening a database containing encrypted credentials or profiles")
		}
	}
	key, err := loadKey(path)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, key: key, path: path}, nil
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := db.ExecContext(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

type Miner struct {
	ID        int64
	Name      string
	Kind      string
	Host      string
	Port      int
	Enabled   bool
	CreatedAt time.Time
}

func (s *Store) ListMiners(ctx context.Context) ([]Miner, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, host, port, enabled, created_at FROM miners ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Miner
	for rows.Next() {
		var m Miner
		var created string
		if err := rows.Scan(&m.ID, &m.Name, &m.Kind, &m.Host, &m.Port, &m.Enabled, &created); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetMiner(ctx context.Context, id int64) (Miner, error) {
	var m Miner
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, kind, host, port, enabled, created_at FROM miners WHERE id = ?`, id).
		Scan(&m.ID, &m.Name, &m.Kind, &m.Host, &m.Port, &m.Enabled, &created)
	if err != nil {
		return Miner{}, err
	}
	m.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return m, nil
}

func (s *Store) CreateMiner(ctx context.Context, name, kind, host string, port int) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO miners (name, kind, host, port, enabled) VALUES (?, ?, ?, ?, 1)`,
		name, kind, host, port)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteMiner(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM miners WHERE id = ?`, id)
	return err
}

func (s *Store) SetMinerEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE miners SET enabled = ? WHERE id = ?`, enabled, id)
	return err
}

// Sample is one poll result. Ok=false means the poll failed (device
// unreachable, bad response); Error carries why, and the numeric fields are
// zero-valued. Storing failed polls too is what lets the UI show "offline
// since" rather than just a gap in the chart.
type Sample struct {
	MinerID         int64
	TS              time.Time
	Ok              bool
	Error           string
	HashrateGHs     float64
	TempC           float64
	VRTempC         float64
	PowerW          float64
	VoltageMV       float64
	FanRPM          int
	FanPercent      float64
	AutoFanMode     int
	SharesAccepted  int64
	SharesRejected  int64
	BestDiff        float64
	BestSessionDiff float64
	UptimeS         int64
	WifiRSSI        int
	RawJSON         string
}

func (s *Store) InsertSample(ctx context.Context, sm Sample) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO samples (
			miner_id, ts, ok, error, hashrate_ghs, temp_c, vr_temp_c, power_w,
			voltage_mv, fan_rpm, fan_percent, auto_fan_mode, shares_accepted,
			shares_rejected, best_diff, best_session_diff, uptime_s, wifi_rssi, raw_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sm.MinerID, sm.TS.UTC().Format(time.RFC3339), sm.Ok, sm.Error,
		sm.HashrateGHs, sm.TempC, sm.VRTempC, sm.PowerW, sm.VoltageMV,
		sm.FanRPM, sm.FanPercent, sm.AutoFanMode, sm.SharesAccepted, sm.SharesRejected,
		sm.BestDiff, sm.BestSessionDiff, sm.UptimeS, sm.WifiRSSI, sm.RawJSON)
	return err
}

func (s *Store) LatestSample(ctx context.Context, minerID int64) (Sample, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT miner_id, ts, ok, error, hashrate_ghs, temp_c, vr_temp_c, power_w,
			voltage_mv, fan_rpm, fan_percent, auto_fan_mode, shares_accepted,
			shares_rejected, best_diff, best_session_diff, uptime_s, wifi_rssi, raw_json
		FROM samples WHERE miner_id = ? ORDER BY ts DESC LIMIT 1`, minerID)
	return scanSampleRow(row)
}

func (s *Store) SamplesSince(ctx context.Context, minerID int64, since time.Time) ([]Sample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT miner_id, ts, ok, error, hashrate_ghs, temp_c, vr_temp_c, power_w,
			voltage_mv, fan_rpm, fan_percent, auto_fan_mode, shares_accepted,
			shares_rejected, best_diff, best_session_diff, uptime_s, wifi_rssi, ''
		FROM samples WHERE miner_id = ? AND ts >= ? ORDER BY ts ASC`,
		minerID, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Sample
	for rows.Next() {
		sm, err := scanSampleRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// PruneSamplesOlderThan deletes samples before cutoff, implementing the
// long-term retention window (default 90 days, see cmd flags).
func (s *Store) PruneSamplesOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM events WHERE ts < ?", cutoff.UTC().Format(time.RFC3339)); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM samples WHERE ts < ?`, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AlertState is the per-miner de-duplication state for the block-found and
// high-difficulty alerts: without it, every poll (every 15s by default)
// would re-send the same email forever.
type AlertState struct {
	LastBlocksFound int64
	HighDiffAlerted bool
}

// GetAlertState returns the zero AlertState (never alerted) for a miner
// with no row yet - the common case for a newly added miner - rather than
// an error, so callers don't need a special first-poll branch.
func (s *Store) GetAlertState(ctx context.Context, minerID int64) (AlertState, error) {
	var st AlertState
	err := s.db.QueryRowContext(ctx,
		`SELECT last_blocks_found, high_diff_alerted FROM alert_state WHERE miner_id = ?`, minerID,
	).Scan(&st.LastBlocksFound, &st.HighDiffAlerted)
	if errors.Is(err, sql.ErrNoRows) {
		return AlertState{}, nil
	}
	return st, err
}

func (s *Store) SetAlertState(ctx context.Context, minerID int64, st AlertState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_state (miner_id, last_blocks_found, high_diff_alerted, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(miner_id) DO UPDATE SET
			last_blocks_found = excluded.last_blocks_found,
			high_diff_alerted = excluded.high_diff_alerted,
			updated_at = excluded.updated_at`,
		minerID, st.LastBlocksFound, st.HighDiffAlerted, time.Now().UTC().Format(time.RFC3339))
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSampleRow(row rowScanner) (Sample, error) {
	var sm Sample
	var ts, errField sql.NullString
	if err := row.Scan(
		&sm.MinerID, &ts, &sm.Ok, &errField, &sm.HashrateGHs, &sm.TempC, &sm.VRTempC,
		&sm.PowerW, &sm.VoltageMV, &sm.FanRPM, &sm.FanPercent, &sm.AutoFanMode, &sm.SharesAccepted,
		&sm.SharesRejected, &sm.BestDiff, &sm.BestSessionDiff, &sm.UptimeS, &sm.WifiRSSI, &sm.RawJSON,
	); err != nil {
		return Sample{}, err
	}
	sm.TS, _ = time.Parse(time.RFC3339, ts.String)
	sm.Error = errField.String
	return sm, nil
}
