package miners

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/yorgof/miner-fleet/internal/alert"
	"github.com/yorgof/miner-fleet/internal/cyd"
	"github.com/yorgof/miner-fleet/internal/store"
)

// Registry owns one poll goroutine per enabled miner and the in-memory
// "latest stats" cache the card grid reads from, so the dashboard never
// waits on a live device or a database round trip.
type Registry struct {
	st *store.Store
	// notifier is never nil - callers pass a Notifier built from a zero
	// Config when alerting isn't configured, and Check() no-ops on that by
	// itself (alert.Config.Enabled()), so there is no nil check needed here.
	notifier *alert.Notifier
	interval time.Duration

	// baseCtx is the process lifetime context (cancelled on shutdown), used
	// as the parent for every poll goroutine. Workers must NOT be derived
	// from a request context: Add() is called from an HTTP handler, and a
	// request's context is cancelled as soon as that request finishes, which
	// would kill a newly added miner's poller almost immediately.
	baseCtx context.Context

	mu      sync.RWMutex
	workers map[int64]context.CancelFunc
	clients map[int64]Client
	latest  map[int64]Stats
}

func NewRegistry(st *store.Store, notifier *alert.Notifier, interval time.Duration) *Registry {
	return &Registry{
		st:       st,
		notifier: notifier,
		interval: interval,
		workers:  make(map[int64]context.CancelFunc),
		clients:  make(map[int64]Client),
		latest:   make(map[int64]Stats),
	}
}

// Start records ctx as the process lifetime context, loads every enabled
// miner from the database, and launches its poller. Call once at startup;
// cancelling ctx stops every current and future worker.
func (r *Registry) Start(ctx context.Context) error {
	r.baseCtx = ctx
	ms, err := r.st.ListMiners(ctx)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.Enabled {
			r.startWorker(m)
		}
	}
	return nil
}

func (r *Registry) startWorker(m store.Miner) {
	client, err := NewClient(m.Kind, m.Host, m.Port)
	if err != nil {
		log.Printf("miner %d (%s): %v", m.ID, m.Name, err)
		return
	}
	if m.Kind == KindCYD {
		if c, e := r.st.Credentials(r.baseCtx, m.ID); e == nil && c.Password != "" {
			client = &cydAdapter{c: cyd.New(m.Host, m.Port, c.Password)}
		}
	}

	workerCtx, cancel := context.WithCancel(r.baseCtx)

	r.mu.Lock()
	r.workers[m.ID] = cancel
	r.clients[m.ID] = client
	r.mu.Unlock()

	go r.pollLoop(workerCtx, m.ID, m.Name, client)
}

func (r *Registry) stopWorker(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel, ok := r.workers[id]; ok {
		cancel()
	}
	delete(r.workers, id)
	delete(r.clients, id)
	delete(r.latest, id)
}

// Add creates the miner in the database and starts polling it immediately.
func (r *Registry) Add(ctx context.Context, name, kind, host string, port int) (store.Miner, error) {
	id, err := r.st.CreateMiner(ctx, name, kind, host, port)
	if err != nil {
		return store.Miner{}, err
	}
	m, err := r.st.GetMiner(ctx, id)
	if err != nil {
		return store.Miner{}, err
	}
	r.startWorker(m)
	return m, nil
}

// Remove stops polling and deletes the miner and its stored samples.
func (r *Registry) Remove(ctx context.Context, id int64) error {
	r.stopWorker(id)
	return r.st.DeleteMiner(ctx, id)
}

func (r *Registry) Miners(ctx context.Context) ([]store.Miner, error) {
	return r.st.ListMiners(ctx)
}

func (r *Registry) Latest(id int64) (Stats, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.latest[id]
	return s, ok
}

func (r *Registry) ClientFor(id int64) (Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[id]
	return c, ok
}

func (r *Registry) pollLoop(ctx context.Context, id int64, name string, client Client) {
	r.pollOnce(ctx, id, name, client)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.pollOnce(ctx, id, name, client)
		}
	}
}

func (r *Registry) pollOnce(ctx context.Context, id int64, name string, client Client) {
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	stats, err := client.FetchStats(pctx)
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	paused := false
	_ = r.st.Setting(ctx, fmt.Sprintf("paused:%d", id), &paused)
	if err != nil {
		previous, _ := r.Latest(id)
		stats = Stats{Ok: false, Paused: paused, Error: err.Error(), RawJSON: previous.RawJSON, Version: previous.Version, Model: previous.Model, LastSuccess: previous.LastSuccess}
	} else {
		stats.Ok = true
		m, _ := r.st.GetMiner(ctx, id)
		Enrich(&stats, m.Kind)
		stats.LastSuccess = now
		d := Data(stats.RawJSON)
		if _, known := d["miningPaused"].(bool); known {
			paused = stats.Paused
		} else if _, known := d["shutdown"].(bool); known {
			paused = stats.Paused
		} else if paused && stats.HashrateGHs > 0 {
			var pauseAt time.Time
			_ = r.st.Setting(ctx, fmt.Sprintf("pause_at:%d", id), &pauseAt)
			if now.Sub(pauseAt) > 30*time.Second {
				paused = false
				_ = r.st.SaveSetting(ctx, fmt.Sprintf("paused:%d", id), false)
			}
		}
		stats.Paused = stats.Paused || paused
	}
	stats.SampledAt = now

	r.mu.Lock()
	if ctx.Err() != nil {
		r.mu.Unlock()
		return
	}
	r.latest[id] = stats
	r.mu.Unlock()

	raw := stats.RawJSON
	if !stats.Ok {
		raw = ""
	}
	sample := store.Sample{
		MinerID:         id,
		TS:              now,
		Ok:              stats.Ok,
		Error:           stats.Error,
		HashrateGHs:     stats.HashrateGHs,
		TempC:           stats.TempC,
		VRTempC:         stats.VRTempC,
		PowerW:          stats.PowerW,
		VoltageMV:       stats.VoltageMV,
		FanRPM:          stats.FanRPM,
		FanPercent:      stats.FanPercent,
		AutoFanMode:     stats.AutoFanMode,
		SharesAccepted:  stats.SharesAccepted,
		SharesRejected:  stats.SharesRejected,
		BestDiff:        stats.BestDiff,
		BestSessionDiff: stats.BestSessionDiff,
		UptimeS:         stats.UptimeS,
		WifiRSSI:        stats.WifiRSSI,
		RawJSON:         raw,
	}
	// Use a background context for the write: the poll's own context may be
	// what just timed out, and a failed poll is exactly the sample worth
	// keeping (it's what makes a miner show "offline" in the UI).
	if err := r.st.InsertSample(context.Background(), sample); err != nil {
		log.Printf("miner %d: insert sample: %v", id, err)
	}

	if stats.Ok {
		r.notifier.Check(context.Background(), id, name, stats.BlocksFound, stats.BestDiff)
	}
}

func (r *Registry) Refresh(ctx context.Context, id int64) error {
	m, err := r.st.GetMiner(ctx, id)
	if err != nil {
		return err
	}
	r.stopWorker(id)
	if m.Enabled {
		r.startWorker(m)
	}
	return nil
}

// StartPruner runs a daily job deleting samples older than retention. It
// blocks until ctx is cancelled, so call it in its own goroutine.
func (r *Registry) StartPruner(ctx context.Context, retention time.Duration) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		cutoff := time.Now().Add(-retention)
		if n, err := r.st.PruneSamplesOlderThan(ctx, cutoff); err != nil {
			log.Printf("prune samples: %v", err)
		} else if n > 0 {
			log.Printf("pruned %d samples older than %s", n, retention)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
