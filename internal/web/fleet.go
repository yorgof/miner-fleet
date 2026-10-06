package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yorgof/miner-fleet/internal/fleet"
	"github.com/yorgof/miner-fleet/internal/miners"
	"github.com/yorgof/miner-fleet/internal/sanitize"
	"github.com/yorgof/miner-fleet/internal/store"
)

func (s *Server) Configure(m *miners.Manager, interval time.Duration) {
	s.manager = m
	s.interval = interval
}
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		if password := os.Getenv("MINER_FLEET_PASSWORD"); password != "" && r.URL.Path != "/healthz" {
			user, pass, ok := r.BasicAuth()
			expected := os.Getenv("MINER_FLEET_USERNAME")
			if expected == "" {
				expected = "admin"
			}
			if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(expected)) != 1 || subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Miner Fleet"`)
				http.Error(w, "login required", 401)
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "cross-site request rejected", 403)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "http" && u.Scheme != "https") {
					http.Error(w, "request origin does not match this dashboard", 403)
					return
				}
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 9<<20)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	op := r.PathValue("operation")
	r.Body = http.MaxBytesReader(w, r.Body, 9<<20)
	values := map[string]any{}
	var upload []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err = r.ParseMultipartForm(9 << 20); err != nil {
			http.Error(w, "invalid upload", 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		f, _, e := r.FormFile("firmware")
		if e != nil {
			http.Error(w, "choose a firmware file", 400)
			return
		}
		defer f.Close()
		upload, err = io.ReadAll(io.LimitReader(f, 8<<20+1))
		if err != nil {
			http.Error(w, "cannot read upload", 400)
			return
		}
	} else {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		desc, e := s.manager.Describe(r.Context(), id, false)
		if e != nil {
			s.notFoundOrError(w, e)
			return
		}
		var c *miners.Control
		for _, v := range desc.Controls {
			if v.ID == op {
				v := v
				c = &v
				break
			}
		}
		if c == nil {
			http.Error(w, "unsupported operation", 400)
			return
		}
		for _, f := range c.Fields {
			value, exists := r.Form[f.Key]
			if !exists || len(value) == 0 || value[0] == "" {
				continue
			}
			raw := value[0]
			switch f.Type {
			case "number":
				v, e := strconv.ParseFloat(raw, 64)
				if e != nil {
					http.Error(w, "invalid "+f.Label, 400)
					return
				}
				values[f.Key] = v
			case "json":
				var v any
				if e := json.Unmarshal([]byte(raw), &v); e != nil {
					http.Error(w, "invalid JSON for "+f.Label, 400)
					return
				}
				values[f.Key] = v
			case "select":
				if raw == "true" || raw == "false" {
					values[f.Key] = raw == "true"
				} else if f.Key == "mode" || f.Key == "stratumProtocol" || f.Key == "fallbackStratumProtocol" {
					if n, e := strconv.ParseFloat(raw, 64); e == nil {
						values[f.Key] = n
					} else {
						values[f.Key] = raw
					}
				} else if n, e := strconv.ParseFloat(raw, 64); e == nil {
					values[f.Key] = n
				} else {
					values[f.Key] = raw
				}
			default:
				values[f.Key] = raw
			}
		}
		if name := strings.TrimSpace(r.FormValue("save_profile")); name != "" {
			if !c.Profile || len(name) > 100 {
				http.Error(w, "this operation cannot be saved as a profile", 400)
				return
			}
			if err = miners.Validate(*c, values); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			err = s.st.SaveProfile(r.Context(), store.Profile{MinerID: id, Name: name, Operation: op, Values: values})
			if err != nil {
				s.serverError(w, err)
				return
			}
			_ = s.st.Event(r.Context(), id, "profile", name, "Saved "+op+" profile", true)
			s.success(w, "Profile saved. The miner was not changed.")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := s.manager.Execute(ctx, id, op, values, upload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="miner-%d-%s.txt"`, id, op))
		w.Write(result)
		return
	}
	s.success(w, "Command accepted. Readings update on the next poll.")
}
func (s *Server) success(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(message))
}
func (s *Server) credentials(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err = s.st.GetMiner(r.Context(), id); err != nil {
		s.notFoundOrError(w, err)
		return
	}
	if err = r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	c, err := s.st.Credentials(r.Context(), id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if r.FormValue("clear") == "1" {
		err = s.st.ClearCredentials(r.Context(), id)
	} else {
		if user := r.FormValue("username"); user != "" {
			c.Username = user
		}
		if pass := r.FormValue("password"); pass != "" {
			c.Password = pass
		}
		if code := r.FormValue("otp"); code != "" {
			c.SessionToken, err = s.manager.OTPSession(r.Context(), id, code)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		c.OTP = ""
		port := 80
		if p := r.FormValue("web_port"); p != "" {
			port, err = strconv.Atoi(p)
			if err != nil || port < 1 || port > 65535 {
				http.Error(w, "invalid web port", 400)
				return
			}
		}
		c.WebPort = port
		if c.Username == "" {
			c.Username = "root"
		}
		err = s.st.SaveCredentials(r.Context(), id, c)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.manager.Invalidate(id)
	_ = s.reg.Refresh(r.Context(), id)
	_ = s.st.Event(r.Context(), id, "credentials", "save", "Connection credentials updated", true)
	s.success(w, "Credentials saved. Reload the page to refresh configuration and controls.")
}
func (s *Server) rawData(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.manager.Describe(r.Context(), id, true)
	if err != nil {
		s.notFoundOrError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="miner-%d-telemetry.json"`, id))
	json.NewEncoder(w).Encode(sanitize.Value(d.Data))
}
func (s *Server) updateMiner(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := s.st.GetMiner(r.Context(), id)
	if err != nil {
		s.notFoundOrError(w, err)
		return
	}
	if r.ParseForm() != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	m.Name = strings.TrimSpace(r.FormValue("name"))
	m.Host = strings.TrimSpace(r.FormValue("host"))
	m.Port, err = strconv.Atoi(r.FormValue("port"))
	if err != nil || m.Port < 0 || m.Port > 65535 || m.Name == "" || !validHost(m.Host) {
		http.Error(w, "enter a valid name, host and port", 400)
		return
	}
	m.Enabled = r.FormValue("enabled") == "1"
	if _, err = miners.NewClient(m.Kind, m.Host, m.Port); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err = s.st.UpdateMiner(r.Context(), m); err != nil {
		s.serverError(w, err)
		return
	}
	muted := r.FormValue("muted") == "1"
	_ = s.st.SaveSetting(r.Context(), fmt.Sprintf("mute:%d", id), muted)
	s.manager.Invalidate(id)
	_ = s.reg.Refresh(r.Context(), id)
	_ = s.st.Event(r.Context(), id, "configuration", "miner", "Miner registration updated", true)
	s.success(w, "Miner registration updated.")
}
func validHost(host string) bool {
	return host != "" && len(host) <= 253 && !strings.ContainsAny(host, "/\\?#@ \t\n\r")
}

func (s *Server) profileApply(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("profile"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.st.Profile(r.Context(), id)
	if err != nil {
		s.notFoundOrError(w, err)
		return
	}
	if _, err = s.manager.Execute(r.Context(), p.MinerID, p.Operation, p.Values, nil); err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	s.success(w, "Profile applied.")
}
func (s *Server) profileDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("profile"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = s.st.DeleteProfile(r.Context(), id); err != nil {
		s.serverError(w, err)
		return
	}
	s.success(w, "Profile removed.")
}

type fleetVM struct {
	Settings            fleet.Settings
	Miners              []store.Miner
	Profiles            []store.Profile
	Schedules           []store.Schedule
	Events              []store.Event
	Health              []store.HealthState
	Energy              fleet.Energy
	EnergyMiners        int
	PowerMiners         int
	NotificationEnabled bool
}

func (s *Server) fleetPage(w http.ResponseWriter, r *http.Request) {
	cfg, err := fleet.LoadSettings(r.Context(), s.st)
	if err != nil {
		s.serverError(w, err)
		return
	}
	ms, err := s.reg.Miners(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	vm := fleetVM{Settings: cfg, Miners: ms, NotificationEnabled: os.Getenv("MINER_FLEET_NTFY_URL") != ""}
	vm.Schedules, err = s.st.Schedules(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	vm.Events, _ = s.st.Events(r.Context(), 0)
	vm.Health, _ = s.st.HealthStates(r.Context())
	vm.Profiles = []store.Profile{}
	now := time.Now()
	start := now.Add(-24 * time.Hour)
	for _, m := range ms {
		p, e := s.st.Profiles(r.Context(), m.ID)
		if e != nil {
			s.serverError(w, e)
			return
		}
		for i := range p {
			p[i].Values = nil
		}
		vm.Profiles = append(vm.Profiles, p...)
		samples, e := s.st.SamplesSince(r.Context(), m.ID, start.Add(-3*s.interval))
		if e != nil {
			s.serverError(w, e)
			return
		}
		energy := fleet.Integrate(samples, cfg.Rate, start, now, s.interval, m.Kind == miners.KindBraiins)
		vm.Energy.KWh += energy.KWh
		vm.Energy.Cost += energy.Cost
		vm.Energy.Estimated = vm.Energy.Estimated || energy.Estimated
		if energy.Hours > 0 {
			vm.EnergyMiners++
		}
		st, _ := s.reg.Latest(m.ID)
		if st.PowerW > 0 {
			vm.PowerMiners++
		}
	}
	s.renderPage(w, "Fleet settings", "fleetPage", vm)
}
func (s *Server) saveFleet(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	v := fleet.DefaultSettings()
	var err error
	v.Rate, err = strconv.ParseFloat(r.FormValue("rate"), 64)
	if err != nil {
		http.Error(w, "invalid electricity rate", 400)
		return
	}
	v.Currency = strings.ToUpper(r.FormValue("currency"))
	v.GraceSeconds, _ = strconv.Atoi(r.FormValue("grace"))
	v.MaxTemp, _ = strconv.ParseFloat(r.FormValue("max_temp"), 64)
	v.RejectPercent, _ = strconv.ParseFloat(r.FormValue("rejects"), 64)
	v.HealthEnabled = r.FormValue("health") == "1"
	v.Notifications = r.FormValue("notifications") == "1"
	if err = fleet.ValidateSettings(v); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err = s.st.SaveSetting(r.Context(), "fleet", v); err != nil {
		s.serverError(w, err)
		return
	}
	_ = s.st.Event(r.Context(), 0, "configuration", "fleet", "Fleet settings updated", true)
	s.success(w, "Fleet settings saved.")
}
func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	v := store.Schedule{Name: strings.TrimSpace(r.FormValue("name")), Timezone: r.FormValue("timezone"), Days: strings.Join(r.Form["days"], ","), At: r.FormValue("at"), Operation: r.FormValue("operation"), Enabled: r.FormValue("enabled") == "1"}
	v.MinerID, _ = strconv.ParseInt(r.FormValue("miner_id"), 10, 64)
	v.ProfileID, _ = strconv.ParseInt(r.FormValue("profile_id"), 10, 64)
	if err := fleet.ValidateSchedule(v); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	m, err := s.st.GetMiner(r.Context(), v.MinerID)
	if err != nil {
		s.notFoundOrError(w, err)
		return
	}
	if v.ProfileID > 0 {
		p, e := s.st.Profile(r.Context(), v.ProfileID)
		if e != nil || p.MinerID != v.MinerID {
			http.Error(w, "choose a profile belonging to this miner", 400)
			return
		}
		v.Operation = p.Operation
	}
	desc, err := s.manager.Describe(r.Context(), m.ID, false)
	if err != nil {
		s.serverError(w, err)
		return
	}
	supported := false
	for _, c := range desc.Controls {
		supported = supported || c.ID == v.Operation
	}
	if !supported {
		http.Error(w, "this miner does not support the scheduled operation", 400)
		return
	}
	if err = s.st.SaveSchedule(r.Context(), v); err != nil {
		s.serverError(w, err)
		return
	}
	_ = s.st.Event(r.Context(), v.MinerID, "schedule", v.Name, "Schedule saved", true)
	s.success(w, "Schedule saved. Reload to see it in the list.")
}
func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("schedule"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = s.st.DeleteSchedule(r.Context(), id); err != nil {
		s.serverError(w, err)
		return
	}
	s.success(w, "Schedule removed.")
}

func (s *Server) exportConfig(w http.ResponseWriter, r *http.Request) {
	v := fleet.Export{Version: 1}
	var err error
	v.Settings, err = fleet.LoadSettings(r.Context(), s.st)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.Miners, err = s.st.ListMiners(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, m := range v.Miners {
		p, e := s.st.Profiles(r.Context(), m.ID)
		if e != nil {
			s.serverError(w, e)
			return
		}
		for i := range p {
			p[i].Values = sanitize.WithoutSecrets(p[i].Values).(map[string]any)
		}
		v.Profiles = append(v.Profiles, p...)
	}
	v.Schedules, err = s.st.Schedules(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="miner-fleet-config.json"`)
	json.NewEncoder(w).Encode(v)
}
func (s *Server) importConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if r.ParseMultipartForm(2<<20) != nil {
		http.Error(w, "invalid configuration upload", 400)
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("config")
	if err != nil {
		http.Error(w, "choose a configuration file", 400)
		return
	}
	defer file.Close()
	var v fleet.Export
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	if err = dec.Decode(&v); err != nil || v.Version != 1 || len(v.Miners) > 200 || len(v.Profiles) > 1000 || len(v.Schedules) > 1000 {
		http.Error(w, "invalid configuration format", 400)
		return
	}
	if err = fleet.ValidateSettings(v.Settings); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Validate the complete document before inserting anything. Import adds miners;
	// it never deletes existing devices or history, and imported schedules are off.
	ids := map[int64]store.Miner{}
	profiles := map[int64]store.Profile{}
	for _, m := range v.Miners {
		if m.ID <= 0 || ids[m.ID].ID != 0 || strings.TrimSpace(m.Name) == "" || !validHost(m.Host) || m.Port < 0 || m.Port > 65535 {
			http.Error(w, "invalid miner registration", 400)
			return
		}
		if _, err = miners.NewClient(m.Kind, m.Host, m.Port); err != nil {
			http.Error(w, "invalid miner kind or address", 400)
			return
		}
		ids[m.ID] = m
	}
	for i := range v.Profiles {
		p := v.Profiles[i]
		if p.Values == nil {
			http.Error(w, "profile values are required", 400)
			return
		}
		p.Values = sanitize.WithoutSecrets(p.Values).(map[string]any)
		v.Profiles[i] = p
		if p.ID <= 0 || profiles[p.ID].ID != 0 || ids[p.MinerID].ID == 0 || p.Name == "" || len(p.Name) > 100 {
			http.Error(w, "invalid profile", 400)
			return
		}
		supported := false
		candidate := miners.Controls(ids[p.MinerID].Kind, map[string]any{})
		if ids[p.MinerID].Kind == miners.KindAxeOS {
			candidate = append(candidate, miners.Controls(miners.KindAxeOS, map[string]any{"fans": []any{}})...)
		}
		for _, c := range candidate {
			if c.ID == p.Operation && c.Profile && miners.Validate(c, p.Values) == nil {
				supported = true
			}
		}
		if !supported {
			http.Error(w, "unsupported profile", 400)
			return
		}
		profiles[p.ID] = p
	}
	for _, a := range v.Schedules {
		if ids[a.MinerID].ID == 0 {
			http.Error(w, "invalid schedule miner", 400)
			return
		}
		if a.ProfileID > 0 && profiles[a.ProfileID].MinerID != a.MinerID {
			http.Error(w, "invalid schedule profile", 400)
			return
		}
		if a.ProfileID == 0 {
			supported := false
			for _, c := range miners.Controls(ids[a.MinerID].Kind, map[string]any{"miningPaused": false}) {
				supported = supported || c.ID == a.Operation
			}
			if !supported {
				http.Error(w, "unsupported schedule operation", 400)
				return
			}
		}
		if err = fleet.ValidateSchedule(a); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	created, err := s.st.ImportConfiguration(r.Context(), v.Miners, v.Profiles, v.Schedules, v.Settings)
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, m := range created {
		if err := s.reg.Refresh(r.Context(), m.ID); err != nil {
			s.serverError(w, err)
			return
		}
	}
	_ = s.st.Event(r.Context(), 0, "configuration", "import", fmt.Sprintf("Imported %d miners; schedules disabled", len(v.Miners)), true)
	s.success(w, "Configuration imported. Existing history is preserved; imported schedules are disabled. Credentials must be entered separately.")
}
func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.Backup(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	defer os.Remove(p)
	w.Header().Set("Content-Disposition", `attachment; filename="miner-fleet-backup.db"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

func (s *Server) toggleSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("schedule"), 10, 64)
	if err != nil || r.ParseForm() != nil {
		http.Error(w, "invalid schedule", 400)
		return
	}
	all, err := s.st.Schedules(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	found := false
	for _, v := range all {
		if v.ID == id {
			found = true
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	enabled := r.FormValue("enabled") == "1"
	if err = s.st.ToggleSchedule(r.Context(), id, enabled); err != nil {
		s.serverError(w, err)
		return
	}
	_ = s.st.Event(r.Context(), 0, "schedule", "toggle", fmt.Sprintf("Schedule %d enabled=%t", id, enabled), true)
	s.success(w, "Schedule updated. Reload to see the current status.")
}
