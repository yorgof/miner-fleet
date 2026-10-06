package fleet

import (
	"context"
	"fmt"
	"math"
	"github.com/yorgof/miner-fleet/internal/alert"
	"github.com/yorgof/miner-fleet/internal/miners"
	"github.com/yorgof/miner-fleet/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnergyExcludesMissingPowerAndOutages(t *testing.T) {
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	samples := []store.Sample{
		{TS: start, Ok: true, PowerW: 1000},
		{TS: start.Add(time.Minute), Ok: true, PowerW: 1000},
		{TS: start.Add(2 * time.Minute), Ok: false, PowerW: 1000},
		{TS: start.Add(3 * time.Minute), Ok: true, PowerW: 1000},
		{TS: start.Add(10 * time.Minute), Ok: true, PowerW: 1000},
		{TS: start.Add(11 * time.Minute), Ok: true, PowerW: 0},
	}
	v := Integrate(samples, 0.20, start, start.Add(time.Hour), time.Minute, true)
	if math.Abs(v.KWh-1.0/60) > 1e-9 || math.Abs(v.Coverage-100.0/60) > 1e-9 || math.Abs(v.Cost-v.KWh*0.2) > 1e-9 || !v.Estimated {
		t.Fatalf("incorrect observed energy: %+v", v)
	}
}
func TestScheduleTimezoneWeekdaysAndDST(t *testing.T) {
	s := store.Schedule{MinerID: 1, Name: "quiet", Timezone: "America/New_York", At: "01:30", Days: "0", Operation: "pause"}
	for _, stamp := range []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"} {
		now, _ := time.Parse(time.RFC3339, stamp)
		slot, due := Due(s, now)
		if !due || slot == "" {
			t.Fatal("DST occurrence lost", stamp)
		}
	}
	a, _ := time.Parse(time.RFC3339, "2026-11-01T05:30:00Z")
	b := a.Add(time.Hour)
	sa, _ := Due(s, a)
	sb, _ := Due(s, b)
	if sa == sb {
		t.Fatal("DST repeated occurrences must have distinct slots")
	}
	if _, due := Due(s, a.Add(time.Minute)); due {
		t.Fatal("missed minute replayed")
	}
	s.Days = "1"
	if _, due := Due(s, a); due {
		t.Fatal("wrong weekday fired")
	}
	s.Timezone = "missing/zone"
	if ValidateSchedule(s) == nil {
		t.Fatal("invalid timezone accepted")
	}
}
func TestNonFiniteSettingsRejected(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1)} {
		s := DefaultSettings()
		s.MaxTemp = v
		if ValidateSettings(s) == nil {
			t.Fatal("nonfinite temperature accepted")
		}
		s = DefaultSettings()
		s.RejectPercent = v
		if ValidateSettings(s) == nil {
			t.Fatal("nonfinite rejection accepted")
		}
	}
}

func TestHealthPersistenceRecoveryAndScheduledFailureIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var hot atomic.Bool
	hot.Store(true)
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Error(w, "fixture rejects control", 503)
			return
		}
		temp := 55
		if hot.Load() {
			temp = 90
		}
		fmt.Fprintf(w, `{"hashRate":600,"temp":%d,"miningPaused":false,"sharesAccepted":12,"uptimeSeconds":100,"fanspeed":30,"fanrpm":3000}`, temp)
	}))
	defer device.Close()
	u, _ := url.Parse(device.URL)
	port, _ := strconv.Atoi(u.Port())
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	notifier := alert.NewNotifier(alert.Config{}, st)
	reg := miners.NewRegistry(st, notifier, time.Hour)
	if err = reg.Start(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := reg.Add(ctx, "test", miners.KindAxeOS, u.Hostname(), port)
	if err != nil {
		t.Fatal(err)
	}
	wait := func(temp float64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			s, _ := reg.Latest(m.ID)
			if s.Ok && s.TempC == temp {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("fixture poll did not finish")
	}
	wait(90)
	mgr := miners.NewManager(st, reg)
	engine := New(st, reg, mgr, notifier)
	now := time.Now().UTC()
	if err = engine.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	states, _ := st.HealthStates(ctx)
	active := 0
	for _, v := range states {
		if v.Active {
			active++
		}
	}
	if active != 1 {
		t.Fatal("temperature incident missing", states)
	}
	// Restarting the engine must not emit the same incident again.
	engine = New(st, reg, mgr, notifier)
	if err = engine.Tick(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ := st.Events(ctx, m.ID)
	if len(events) != 1 {
		t.Fatal("incident duplicate after process restart", events)
	}
	hot.Store(false)
	if err = reg.Refresh(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	wait(55)
	if err = engine.Tick(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, _ = st.Events(ctx, m.ID)
	if len(events) != 2 || !events[0].Success {
		t.Fatal("recovery not recorded", events)
	}
	s := store.Schedule{MinerID: m.ID, Name: "pause fixture", Timezone: "UTC", Days: strconv.Itoa(int(now.Weekday())), At: now.Format("15:04"), Operation: "pause", Enabled: true}
	if err = st.SaveSchedule(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err = engine.Tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	schedules, _ := st.Schedules(ctx)
	if len(schedules) != 1 || schedules[0].LastRun == "" || schedules[0].LastResult == "Completed" {
		t.Fatal("failed schedule result not persisted", schedules)
	}
	before, _ := st.Events(ctx, m.ID)
	if err = engine.Tick(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after, _ := st.Events(ctx, m.ID)
	if len(after) != len(before) {
		t.Fatal("failed scheduled command retried in same time slot")
	}
}
