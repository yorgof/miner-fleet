// Package fleet manages health incidents and explicitly enabled schedules.
package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/yorgof/miner-fleet/internal/alert"
	"github.com/yorgof/miner-fleet/internal/miners"
	"github.com/yorgof/miner-fleet/internal/store"
)

type Settings struct {
	Rate          float64 `json:"electricity_rate"`
	Currency      string  `json:"currency"`
	GraceSeconds  int     `json:"grace_seconds"`
	MaxTemp       float64 `json:"max_temperature"`
	RejectPercent float64 `json:"reject_percent"`
	HealthEnabled bool    `json:"health_enabled"`
	Notifications bool    `json:"notifications"`
}

func DefaultSettings() Settings {
	return Settings{Currency: "USD", GraceSeconds: 180, MaxTemp: 80, RejectPercent: 5, HealthEnabled: true}
}
func LoadSettings(ctx context.Context, st *store.Store) (Settings, error) {
	v := DefaultSettings()
	err := st.Setting(ctx, "fleet", &v)
	return v, err
}
func ValidateSettings(v Settings) error {
	if math.IsNaN(v.Rate) || math.IsInf(v.Rate, 0) || v.Rate < 0 || v.Rate > 100 || len(v.Currency) != 3 || v.GraceSeconds < 30 || v.GraceSeconds > 86400 || math.IsNaN(v.MaxTemp) || math.IsInf(v.MaxTemp, 0) || math.IsNaN(v.RejectPercent) || math.IsInf(v.RejectPercent, 0) || v.MaxTemp < 1 || v.MaxTemp > 110 || v.RejectPercent < 0 || v.RejectPercent > 100 {
		return fmt.Errorf("invalid fleet settings")
	}
	return nil
}

type Engine struct {
	Store    *store.Store
	Registry *miners.Registry
	Manager  *miners.Manager
	Notifier *alert.Notifier
	started  time.Time
	previous map[int64]miners.Stats
	recent   map[int64][]miners.Stats
}

func New(st *store.Store, reg *miners.Registry, mgr *miners.Manager, n *alert.Notifier) *Engine {
	return &Engine{Store: st, Registry: reg, Manager: mgr, Notifier: n, started: time.Now(), previous: map[int64]miners.Stats{}, recent: map[int64][]miners.Stats{}}
}
func (e *Engine) Run(ctx context.Context) {
	go e.refreshMetadata(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.Tick(ctx, time.Now()); err != nil {
				_ = e.Store.Event(ctx, 0, "fleet", "monitor", err.Error(), false)
			}
		}
	}
}
func (e *Engine) Tick(ctx context.Context, now time.Time) error {
	cfg, err := LoadSettings(ctx, e.Store)
	if err != nil {
		return err
	}
	ms, err := e.Registry.Miners(ctx)
	if err != nil {
		return err
	}
	saved, err := e.Store.HealthStates(ctx)
	if err != nil {
		return err
	}
	states := map[string]store.HealthState{}
	for _, s := range saved {
		states[fmt.Sprintf("%d:%s", s.MinerID, s.Kind)] = s
	}
	for _, m := range ms {
		s, _ := e.Registry.Latest(m.ID)
		muted := !m.Enabled
		_ = e.Store.Setting(ctx, fmt.Sprintf("mute:%d", m.ID), &muted)
		muted = muted || !m.Enabled
		issues := map[string]string{}
		if cfg.HealthEnabled && !muted {
			if !s.Ok && !s.Paused {
				issues["offline"] = "Device is unreachable"
			}
			if s.Ok {
				if s.HashrateGHs <= 0 && !s.Paused {
					issues["no_hashing"] = "Device is reachable but has no hashrate"
				}
				if s.PoolKnown && !s.PoolConnected && !s.Paused {
					issues["pool"] = "Mining pool is disconnected"
				}
				if s.TempC >= cfg.MaxTemp {
					issues["temperature"] = fmt.Sprintf("Chip temperature %.1f°C exceeds %.1f°C", s.TempC, cfg.MaxTemp)
				}
				old, exists := e.previous[m.ID]
				window := e.recent[m.ID]
				for len(window) > 1 && now.Sub(window[0].SampledAt) > 3*time.Minute {
					window = window[1:]
				}
				window = append(window, s)
				e.recent[m.ID] = window
				base := window[0]
				da, dr := s.SharesAccepted-base.SharesAccepted, s.SharesRejected-base.SharesRejected
				if exists && s.Ok && old.Ok && da >= 0 && dr >= 0 && da+dr >= 3 {
					pct := 100 * float64(dr) / float64(da+dr)
					if pct >= cfg.RejectPercent && dr > 0 {
						issues["rejects"] = fmt.Sprintf("Recent share rejection rate %.1f%%", pct)
					}
				}
				d := miners.Data(s.RawJSON)
				_, rpmKnown := d["fanrpm"]
				fanStall := rpmKnown && s.FanPercent > 10 && s.FanRPM == 0
				if fans, ok := d["fans"].([]any); ok {
					for _, v := range fans {
						if f, ok := v.(map[string]any); ok {
							rpm, hasRPM := f["rpm"].(float64)
							speed, _ := f["speedPerc"].(float64)
							if hasRPM && speed > 10 && rpm == 0 {
								fanStall = true
							}
						}
					}
				}
				if response, ok := d["fans"].(map[string]any); ok {
					if entries, ok := response["FANS"].([]any); ok {
						for _, v := range entries {
							if fan, ok := v.(map[string]any); ok {
								rpm, hasRPM := fan["RPM"].(float64)
								speed, _ := fan["Speed"].(float64)
								if hasRPM && speed > 10 && rpm == 0 {
									fanStall = true
								}
							}
						}
					}
				}
				if !s.Paused && fanStall {
					issues["fan"] = "Fan is commanded on but reports zero RPM"
				}
				if old.Ok && s.UptimeS+30 < old.UptimeS {
					_ = e.Store.Event(ctx, m.ID, "health", "restart", "Miner uptime reset", true)
				}
			}
		}
		for _, kind := range []string{"offline", "no_hashing", "pool", "temperature", "rejects", "fan"} {
			key := fmt.Sprintf("%d:%s", m.ID, kind)
			prev := states[key]
			prev.MinerID = m.ID
			prev.Kind = kind
			message, bad := issues[kind]
			if bad {
				if prev.FirstSeen == "" {
					prev.FirstSeen = now.UTC().Format(time.RFC3339)
				}
				first, _ := time.Parse(time.RFC3339, prev.FirstSeen)
				grace := time.Duration(cfg.GraceSeconds) * time.Second
				if kind == "temperature" {
					grace = 0
				}
				if !prev.Active && now.Sub(first) >= grace {
					prev.Active = true
					prev.Message = message
					if err := e.Store.Event(ctx, m.ID, "health", kind, message, false); err != nil {
						return err
					}
					if cfg.Notifications {
						e.Notifier.Health(ctx, m.ID, m.Name, kind, message, false)
					}
				}
				prev.Message = message
			} else {
				if prev.Active {
					if err := e.Store.Event(ctx, m.ID, "health", kind, "Recovered: "+prev.Message, true); err != nil {
						return err
					}
					if cfg.Notifications {
						e.Notifier.Health(ctx, m.ID, m.Name, kind, "Recovered", true)
					}
				}
				prev.Active = false
				prev.FirstSeen = ""
				prev.Message = ""
			}
			if err := e.Store.SaveHealth(ctx, prev); err != nil {
				return err
			}
		}
		e.previous[m.ID] = s
	}
	schedules, err := e.Store.Schedules(ctx)
	if err != nil {
		return err
	}
	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		slot, due := Due(s, now)
		if !due {
			continue
		}
		claimed, err := e.Store.ClaimSchedule(ctx, s.ID, slot)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		miner, err := e.Store.GetMiner(ctx, s.MinerID)
		if err == nil && !miner.Enabled {
			err = fmt.Errorf("miner polling is disabled")
		}
		op, v := s.Operation, map[string]any{}
		if err == nil && s.ProfileID > 0 {
			var p store.Profile
			p, err = e.Store.Profile(ctx, s.ProfileID)
			if err == nil {
				if p.MinerID != s.MinerID {
					err = fmt.Errorf("profile belongs to a different miner")
				} else {
					op, v = p.Operation, p.Values
				}
			}
		}
		if err == nil {
			aCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err = e.Manager.Execute(aCtx, s.MinerID, op, v, nil)
			cancel()
		}
		result := "Completed"
		if err != nil {
			result = err.Error()
		}
		_ = e.Store.ScheduleResult(ctx, s.ID, result)
		_ = e.Store.Event(ctx, s.MinerID, "schedule", s.Name, result, err == nil)
	}
	return nil
}

// Due uses a date/time/UTC-offset slot. Fall-back DST minutes fire once per
// actual occurrence, missed minutes aren't replayed after a desktop wakes.
func Due(s store.Schedule, now time.Time) (string, bool) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return "", false
	}
	local := now.In(loc)
	if local.Format("15:04") != s.At {
		return "", false
	}
	day := strconv.Itoa(int(local.Weekday()))
	allowed := false
	for _, d := range strings.Split(s.Days, ",") {
		allowed = allowed || d == day
	}
	return local.Format("2006-01-02T15:04Z07:00"), allowed
}
func ValidateSchedule(s store.Schedule) error {
	if s.MinerID <= 0 || strings.TrimSpace(s.Name) == "" || len(s.Name) > 100 {
		return fmt.Errorf("miner and schedule name are required")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("choose an IANA timezone")
	}
	if _, err := time.Parse("15:04", s.At); err != nil {
		return fmt.Errorf("time must be HH:MM")
	}
	if s.Days == "" {
		return fmt.Errorf("select at least one day")
	}
	for _, d := range strings.Split(s.Days, ",") {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 || n > 6 {
			return fmt.Errorf("invalid schedule days")
		}
	}
	if s.ProfileID == 0 && s.Operation != "pause" && s.Operation != "resume" && s.Operation != "restart" {
		return fmt.Errorf("choose pause, resume, restart, or a saved profile")
	}
	return nil
}

type Energy struct {
	KWh       float64
	Cost      float64
	Hours     float64
	Coverage  float64
	Estimated bool
}

// Integrate only adjacent, successful readings. Long gaps and offline polls
// contribute no energy; coverage exposes incomplete observation rather than
// assuming the miner ran throughout missing history.
func Integrate(samples []store.Sample, rate float64, start, end time.Time, interval time.Duration, estimated bool) Energy {
	out := Energy{Estimated: estimated}
	for i := 1; i < len(samples); i++ {
		a, b := samples[i-1], samples[i]
		dt := b.TS.Sub(a.TS)
		if !a.Ok || !b.Ok || dt <= 0 || dt > 3*interval || a.PowerW <= 0 || b.PowerW <= 0 {
			continue
		}
		from, to := a.TS, b.TS
		if from.Before(start) {
			from = start
		}
		if to.After(end) {
			to = end
		}
		if !to.After(from) {
			continue
		}
		h := to.Sub(from).Hours()
		out.Hours += h
		out.KWh += (a.PowerW + b.PowerW) / 2 * h / 1000
	}
	out.Cost = out.KWh * rate
	if h := end.Sub(start).Hours(); h > 0 {
		out.Coverage = 100 * out.Hours / h
	}
	return out
}

// Export contains no credentials, notification tokens, secrets or history.
type Export struct {
	Version   int              `json:"version"`
	Settings  Settings         `json:"settings"`
	Miners    []store.Miner    `json:"miners"`
	Profiles  []store.Profile  `json:"profiles"`
	Schedules []store.Schedule `json:"schedules"`
}

func Encode(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) }

// Slow metadata reads run separately so schedules and health checks stay timely.
func (e *Engine) refreshMetadata(ctx context.Context) {
	for {
		ms, err := e.Registry.Miners(ctx)
		if err == nil {
			for _, m := range ms {
				if !m.Enabled || ctx.Err() != nil {
					continue
				}
				pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				_, _ = e.Manager.Describe(pctx, m.ID, true)
				cancel()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}
