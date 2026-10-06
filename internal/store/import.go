package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ImportConfiguration adds registrations and settings in one transaction.
// It never replaces existing miners, samples, credentials or schedules.
func (s *Store) ImportConfiguration(ctx context.Context, ms []Miner, ps []Profile, ss []Schedule, settings any) ([]Miner, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	idMap := map[int64]int64{}
	profileMap := map[int64]int64{}
	out := []Miner{}
	for _, m := range ms {
		res, e := tx.ExecContext(ctx, "INSERT INTO miners(name,kind,host,port,enabled) VALUES (?,?,?,?,?)", m.Name, m.Kind, m.Host, m.Port, m.Enabled)
		if e != nil {
			return nil, e
		}
		id, e := res.LastInsertId()
		if e != nil {
			return nil, e
		}
		idMap[m.ID] = id
		m.ID = id
		out = append(out, m)
	}
	for _, p := range ps {
		b, e := s.seal(p.Values)
		if e != nil {
			return nil, e
		}
		minerID, ok := idMap[p.MinerID]
		if !ok {
			return nil, fmt.Errorf("profile miner missing")
		}
		res, e := tx.ExecContext(ctx, "INSERT INTO profiles(miner_id,name,operation,ciphertext,created_at) VALUES (?,?,?,?,?)", minerID, p.Name, p.Operation, b, time.Now().UTC().Format(time.RFC3339))
		if e != nil {
			return nil, e
		}
		id, e := res.LastInsertId()
		if e != nil {
			return nil, e
		}
		profileMap[p.ID] = id
	}
	for _, a := range ss {
		minerID, ok := idMap[a.MinerID]
		if !ok {
			return nil, fmt.Errorf("schedule miner missing")
		}
		var profile any
		if a.ProfileID > 0 {
			v, ok := profileMap[a.ProfileID]
			if !ok {
				return nil, fmt.Errorf("schedule profile missing")
			}
			profile = v
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO schedules(miner_id,name,timezone,days,at,operation,profile_id,enabled) VALUES (?,?,?,?,?,?,?,0)", minerID, a.Name, a.Timezone, a.Days, a.At, a.Operation, profile)
		if e != nil {
			return nil, e
		}
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO fleet_settings VALUES ('fleet',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(b)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
