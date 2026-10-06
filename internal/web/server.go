// Package web renders the dashboard: server-rendered html/template pages,
// HTMX for live updates and form submissions, no client-side framework.
package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yorgof/miner-fleet/internal/fleet"
	"github.com/yorgof/miner-fleet/internal/miners"
	"github.com/yorgof/miner-fleet/internal/store"
)

type Server struct {
	reg      *miners.Registry
	st       *store.Store
	tmpl     *template.Template
	static   http.Handler
	manager  *miners.Manager
	interval time.Duration
}

func NewServer(reg *miners.Registry, st *store.Store, templatesFS, staticFS fs.FS) (*Server, error) {
	funcs := template.FuncMap{
		"fmtHashrate": fmtHashrate,
		"fmtTemp":     fmtTemp,
		"fmtPower":    fmtPower,
		"fmtFan":      fmtFan,
		"fmtDuration": fmtDuration,
		"fmtDiff":     fmtDiff,
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "Waiting for first poll"
			}
			return t.Local().Format("Jan 2 15:04:05")
		},
		"kindLabel": func(k string) string {
			switch k {
			case "axeos":
				return "AxeOS"
			case "cyd":
				return "CYD"
			case "braiins":
				return "Braiins OS"
			default:
				return "Avalon"
			}
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		reg:     reg,
		st:      st,
		tmpl:    tmpl,
		static:  http.FileServer(http.FS(staticFS)),
		manager: miners.NewManager(st, reg), interval: 15 * time.Second,
	}, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", s.static)
	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /partials/cards", s.partialCards)

	mux.HandleFunc("GET /miners/new", s.newMinerForm)
	mux.HandleFunc("POST /miners", s.createMiner)
	mux.HandleFunc("GET /miners/{id}", s.minerDetail)
	mux.HandleFunc("DELETE /miners/{id}", s.deleteMiner)
	mux.HandleFunc("GET /partials/miner/{id}", s.partialMiner)
	mux.HandleFunc("POST /miners/{id}/restart", s.restartMiner)
	mux.HandleFunc("POST /miners/{id}/fan", s.setFan)
	mux.HandleFunc("POST /miners/{id}/autofan", s.setAutoFan)
	mux.HandleFunc("POST /miners/{id}/control/{operation}", s.control)
	mux.HandleFunc("POST /miners/{id}/credentials", s.credentials)
	mux.HandleFunc("POST /miners/{id}/registration", s.updateMiner)
	mux.HandleFunc("GET /miners/{id}/data", s.rawData)
	mux.HandleFunc("POST /profiles/{profile}/apply", s.profileApply)
	mux.HandleFunc("POST /profiles/{profile}/delete", s.profileDelete)
	mux.HandleFunc("GET /fleet", s.fleetPage)
	mux.HandleFunc("POST /fleet/settings", s.saveFleet)
	mux.HandleFunc("POST /fleet/schedules", s.saveSchedule)
	mux.HandleFunc("POST /fleet/schedules/{schedule}/delete", s.deleteSchedule)
	mux.HandleFunc("POST /fleet/schedules/{schedule}/toggle", s.toggleSchedule)
	mux.HandleFunc("GET /fleet/export", s.exportConfig)
	mux.HandleFunc("POST /fleet/import", s.importConfig)
	mux.HandleFunc("POST /fleet/backup", s.backup)

	return s.protect(mux)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

type cardVM struct {
	Miner  store.Miner
	Stats  miners.Stats
	Health []store.HealthState
	Status string
}

// summaryVM is the fleet-wide totals shown above the card grid. It only
// sums miners currently reporting Ok=true - an offline miner's last-known
// numbers already disappear from its own card (see Registry.pollOnce), so
// leaving it out of the totals too is the consistent behavior, not a
// separate special case.
type summaryVM struct {
	TotalCount       int
	OnlineCount      int
	TotalHashrateGHs float64
	TotalPowerW      float64
	TotalBlocksFound int64
	PowerCount       int
	Estimated        bool
	HealthCount      int
}

func (s *Server) cardVMs(r *http.Request) ([]cardVM, summaryVM, error) {
	ms, err := s.reg.Miners(r.Context())
	if err != nil {
		return nil, summaryVM{}, err
	}
	out := make([]cardVM, 0, len(ms))
	sum := summaryVM{TotalCount: len(ms)}
	health, _ := s.st.HealthStates(r.Context())
	for _, m := range ms {
		st, _ := s.reg.Latest(m.ID)
		desc, _ := s.manager.Describe(r.Context(), m.ID, false)
		st = miners.WithMetadata(st, desc.Data)
		vm := cardVM{Miner: m, Stats: st, Status: "Offline"}
		if !m.Enabled {
			vm.Status = "Polling disabled"
		}
		if st.Ok {
			vm.Status = "Mining"
			if st.Paused {
				vm.Status = "Paused"
			} else if st.HashrateGHs <= 0 {
				vm.Status = "No hashing"
			} else if st.PoolKnown && !st.PoolConnected {
				vm.Status = "Pool offline"
			}
		}
		for _, h := range health {
			if h.MinerID == m.ID && h.Active && m.Enabled {
				vm.Health = append(vm.Health, h)
				sum.HealthCount++
			}
		}
		out = append(out, vm)
		if st.Ok {
			sum.OnlineCount++
			sum.TotalHashrateGHs += st.HashrateGHs
			sum.TotalPowerW += st.PowerW
			if st.PowerW > 0 {
				sum.PowerCount++
				sum.Estimated = sum.Estimated || m.Kind == miners.KindBraiins
			}
			sum.TotalBlocksFound += st.BlocksFound
		}
	}
	return out, sum, nil
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	vms, sum, err := s.cardVMs(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPage(w, "Miners", "cards", map[string]any{"Miners": vms, "Summary": sum})
}

func (s *Server) partialCards(w http.ResponseWriter, r *http.Request) {
	vms, sum, err := s.cardVMs(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragment(w, "cards", map[string]any{"Miners": vms, "Summary": sum})
}

func (s *Server) newMinerForm(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "Add miner", "newForm", nil)
}

func (s *Server) createMiner(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	kind := r.FormValue("kind")
	host := strings.TrimSpace(r.FormValue("host"))
	if name == "" || !validHost(host) {
		http.Error(w, "name and host are required", http.StatusBadRequest)
		return
	}
	if kind != miners.KindAxeOS && kind != miners.KindAvalonCGMiner && kind != miners.KindCYD && kind != miners.KindBraiins {
		http.Error(w, "unknown kind", http.StatusBadRequest)
		return
	}

	port := 0
	if p := strings.TrimSpace(r.FormValue("port")); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil || v <= 0 || v > 65535 {
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}
		port = v
	}

	if _, err := miners.NewClient(kind, host, port); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.reg.Add(r.Context(), name, kind, host, port); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type detailVM struct {
	Miner          store.Miner
	Stats          miners.Stats
	HashrateSpark  template.HTML
	TempSpark      template.HTML
	PowerSpark     template.HTML
	Groups         []dataGroup
	Controls       []miners.Control
	HasCredentials bool
	ControlNote    string
	DeviceURL      string
	Profiles       []store.Profile
	Events         []store.Event
	Health         []store.HealthState
	Energy         fleet.Energy
	Settings       fleet.Settings
	Muted          bool
	Rows           int
}

func (s *Server) buildDetailVM(r *http.Request, id int64) (detailVM, error) {
	m, err := s.st.GetMiner(r.Context(), id)
	if err != nil {
		return detailVM{}, err
	}
	st, _ := s.reg.Latest(id)
	desc, err := s.manager.Describe(r.Context(), id, false)
	if err != nil {
		return detailVM{}, err
	}
	creds, err := s.st.Credentials(r.Context(), id)
	if err != nil {
		return detailVM{}, err
	}

	samples, err := s.st.SamplesSince(r.Context(), id, time.Now().Add(-24*time.Hour))
	if err != nil {
		return detailVM{}, err
	}
	st = miners.WithMetadata(st, desc.Data)
	var hashrates, temps, powers []float64
	for _, sm := range samples {
		if !sm.Ok {
			continue
		}
		hashrates = append(hashrates, sm.HashrateGHs)
		temps = append(temps, sm.TempC)
		if sm.PowerW > 0 {
			powers = append(powers, sm.PowerW)
		}
	}

	var peak float64
	for _, v := range hashrates {
		peak = max(peak, v)
	}
	scale, unit := hashrateUnit(peak)
	for i := range hashrates {
		hashrates[i] /= scale
	}
	vm := detailVM{
		Miner:         m,
		Stats:         st,
		HashrateSpark: sparkline(hashrates, " "+unit),
		TempSpark:     sparkline(temps, "°C"),
		PowerSpark:    sparkline(powers, " W"), Groups: grouped(desc.Data), Controls: desc.Controls, HasCredentials: desc.Credentials, ControlNote: desc.Note, DeviceURL: miners.DeviceURL(m, creds), Rows: len(samples),
	}
	vm.Profiles, err = s.st.Profiles(r.Context(), id)
	if err != nil {
		return detailVM{}, err
	}
	for i := range vm.Profiles {
		vm.Profiles[i].Values = nil
	}
	vm.Events, _ = s.st.Events(r.Context(), id)
	allHealth, _ := s.st.HealthStates(r.Context())
	for _, h := range allHealth {
		if h.MinerID == id && h.Active {
			vm.Health = append(vm.Health, h)
		}
	}
	vm.Settings, err = fleet.LoadSettings(r.Context(), s.st)
	if err != nil {
		return detailVM{}, err
	}
	now := time.Now()
	vm.Energy = fleet.Integrate(samples, vm.Settings.Rate, now.Add(-24*time.Hour), now, s.interval, m.Kind == miners.KindBraiins)
	_ = s.st.Setting(r.Context(), fmt.Sprintf("mute:%d", id), &vm.Muted)
	return vm, nil
}

func (s *Server) minerDetail(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	_, _ = s.manager.Describe(pctx, id, true)
	cancel()
	vm, err := s.buildDetailVM(r, id)
	if err != nil {
		s.notFoundOrError(w, err)
		return
	}
	s.renderPage(w, vm.Miner.Name, "minerBody", vm)
}

func (s *Server) partialMiner(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	vm, err := s.buildDetailVM(r, id)
	if err != nil {
		s.notFoundOrError(w, err)
		return
	}
	s.renderFragment(w, "minerTelemetry", vm)
}

func (s *Server) deleteMiner(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.reg.Remove(r.Context(), id); err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) restartMiner(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.manager.Execute(r.Context(), id, "restart", map[string]any{}, nil); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setFan(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	percent, err := strconv.Atoi(r.FormValue("percent"))
	if err != nil || percent < 0 || percent > 100 {
		http.Error(w, "percent must be 0-100", http.StatusBadRequest)
		return
	}
	values := map[string]any{"autofanspeed": float64(0), "manualFanSpeed": float64(percent)}

	if _, err := s.manager.Execute(r.Context(), id, "cooling", values, nil); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setAutoFan(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") == "1"
	mode := float64(0)
	if enabled {
		mode = 1
		stats, _ := s.reg.Latest(id)
		if miners.Nerd(miners.Data(stats.RawJSON)) {
			mode = 2
		}
	}
	if _, err := s.manager.Execute(r.Context(), id, "cooling", map[string]any{"autofanspeed": mode}, nil); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func idParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func (s *Server) notFoundOrError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, nil)
		return
	}
	s.serverError(w, err)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (s *Server) renderPage(w http.ResponseWriter, title, fragmentName string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, fragmentName, data); err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(w, "page", map[string]any{
		"Title": title,
		"Body":  template.HTML(buf.String()),
	})
}
