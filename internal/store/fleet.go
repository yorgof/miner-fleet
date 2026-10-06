package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func loadKey(path string) ([]byte, error) {
	p := path + ".key"
	key, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(key)
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid credential encryption key")
	}
	if err := os.Chmod(p, 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *Store) seal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	n := make([]byte, g.NonceSize())
	if _, err = rand.Read(n); err != nil {
		return nil, err
	}
	return g.Seal(n, n, b, nil), nil
}
func (s *Store) unseal(b []byte, v any) error {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(b) < g.NonceSize() {
		return fmt.Errorf("invalid encrypted record")
	}
	plain, err := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

type Credentials struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	WebPort      int    `json:"web_port"`
	OTP          string `json:"otp,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
}

func (s *Store) Credentials(ctx context.Context, id int64) (Credentials, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, "SELECT ciphertext FROM credentials WHERE miner_id=?", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Credentials{}, nil
	}
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	err = s.unseal(b, &c)
	return c, err
}
func (s *Store) SaveCredentials(ctx context.Context, id int64, c Credentials) error {
	b, err := s.seal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO credentials VALUES (?,?) ON CONFLICT(miner_id) DO UPDATE SET ciphertext=excluded.ciphertext", id, b)
	return err
}
func (s *Store) ClearCredentials(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM credentials WHERE miner_id=?", id)
	return err
}
func (s *Store) Setting(ctx context.Context, key string, v any) error {
	var b string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM fleet_settings WHERE key=?", key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(b), v)
}
func (s *Store) SaveSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO fleet_settings VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(b))
	return err
}

type Profile struct {
	ID        int64
	MinerID   int64
	Name      string
	Operation string
	Values    map[string]any `json:"values,omitempty"`
	CreatedAt string
}

func (s *Store) SaveProfile(ctx context.Context, p Profile) error {
	b, err := s.seal(p.Values)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO profiles(miner_id,name,operation,ciphertext,created_at) VALUES (?,?,?,?,?) ON CONFLICT(miner_id,name) DO UPDATE SET operation=excluded.operation,ciphertext=excluded.ciphertext`, p.MinerID, p.Name, p.Operation, b, time.Now().UTC().Format(time.RFC3339))
	return err
}
func (s *Store) Profiles(ctx context.Context, id int64) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,miner_id,name,operation,ciphertext,created_at FROM profiles WHERE miner_id=? ORDER BY name", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		var p Profile
		var b []byte
		if err = rows.Scan(&p.ID, &p.MinerID, &p.Name, &p.Operation, &b, &p.CreatedAt); err != nil {
			return nil, err
		}
		if err = s.unseal(b, &p.Values); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Profile(ctx context.Context, id int64) (Profile, error) {
	var p Profile
	var b []byte
	err := s.db.QueryRowContext(ctx, "SELECT id,miner_id,name,operation,ciphertext,created_at FROM profiles WHERE id=?", id).Scan(&p.ID, &p.MinerID, &p.Name, &p.Operation, &b, &p.CreatedAt)
	if err != nil {
		return p, err
	}
	err = s.unseal(b, &p.Values)
	return p, err
}
func (s *Store) DeleteProfile(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM profiles WHERE id=?", id)
	return err
}

type Schedule struct {
	ID         int64
	MinerID    int64
	Name       string
	Timezone   string
	Days       string
	At         string
	Operation  string
	ProfileID  int64
	Enabled    bool
	LastRun    string
	LastResult string
}

func (s *Store) ToggleSchedule(ctx context.Context, id int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, "UPDATE schedules SET enabled=? WHERE id=?", enabled, id)
	return err
}
func (s *Store) Schedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,miner_id,name,timezone,days,at,operation,coalesce(profile_id,0),enabled,last_run,last_result FROM schedules ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		var v Schedule
		if err = rows.Scan(&v.ID, &v.MinerID, &v.Name, &v.Timezone, &v.Days, &v.At, &v.Operation, &v.ProfileID, &v.Enabled, &v.LastRun, &v.LastResult); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SaveSchedule(ctx context.Context, v Schedule) error {
	var profile any
	if v.ProfileID > 0 {
		profile = v.ProfileID
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO schedules(miner_id,name,timezone,days,at,operation,profile_id,enabled) VALUES (?,?,?,?,?,?,?,?)", v.MinerID, v.Name, v.Timezone, v.Days, v.At, v.Operation, profile, v.Enabled)
	return err
}
func (s *Store) DeleteSchedule(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM schedules WHERE id=?", id)
	return err
}

// ClaimSchedule is a durable claim before device I/O: a restart must not replay a disruptive operation.
func (s *Store) ClaimSchedule(ctx context.Context, id int64, slot string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE schedules SET last_run=?,last_result='Running' WHERE id=? AND last_run<>? AND enabled=1", slot, id, slot)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
func (s *Store) ScheduleResult(ctx context.Context, id int64, result string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE schedules SET last_result=? WHERE id=?", result, id)
	return err
}

type Event struct {
	ID        int64
	MinerID   int64
	TS        string
	Category  string
	Operation string
	Detail    string
	Success   bool
}

func (s *Store) Event(ctx context.Context, id int64, category, op, detail string, success bool) error {
	var miner any
	if id > 0 {
		miner = id
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO events(miner_id,ts,category,operation,detail,success) VALUES (?,?,?,?,?,?)", miner, time.Now().UTC().Format(time.RFC3339), category, op, detail, success)
	return err
}
func (s *Store) Events(ctx context.Context, id int64) ([]Event, error) {
	q := "SELECT id,coalesce(miner_id,0),ts,category,operation,detail,success FROM events"
	args := []any{}
	if id > 0 {
		q += " WHERE miner_id=?"
		args = append(args, id)
	}
	q += " ORDER BY id DESC LIMIT 100"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var v Event
		if err = rows.Scan(&v.ID, &v.MinerID, &v.TS, &v.Category, &v.Operation, &v.Detail, &v.Success); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type HealthState struct {
	MinerID   int64
	Kind      string
	FirstSeen string
	Active    bool
	Message   string
}

func (s *Store) HealthStates(ctx context.Context) ([]HealthState, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT miner_id,kind,first_seen,active,message FROM health_state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HealthState{}
	for rows.Next() {
		var v HealthState
		if err = rows.Scan(&v.MinerID, &v.Kind, &v.FirstSeen, &v.Active, &v.Message); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SaveHealth(ctx context.Context, v HealthState) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO health_state VALUES (?,?,?,?,?) ON CONFLICT(miner_id,kind) DO UPDATE SET first_seen=excluded.first_seen,active=excluded.active,message=excluded.message", v.MinerID, v.Kind, v.FirstSeen, v.Active, v.Message)
	return err
}
func (s *Store) Backup(ctx context.Context) (string, error) {
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "fleet-*.db")
	if err != nil {
		return "", err
	}
	p := f.Name()
	f.Close()
	os.Remove(p)
	if _, err = s.db.ExecContext(ctx, "VACUUM INTO ?", p); err != nil {
		return "", err
	}
	if err = os.Chmod(p, 0600); err != nil {
		return "", err
	}
	return p, nil
}
func (s *Store) UpdateMiner(ctx context.Context, m Miner) error {
	_, err := s.db.ExecContext(ctx, "UPDATE miners SET name=?,host=?,port=?,enabled=? WHERE id=?", m.Name, m.Host, m.Port, m.Enabled, m.ID)
	return err
}
