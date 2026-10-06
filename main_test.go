package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yorgof/miner-fleet/internal/alert"
	"github.com/yorgof/miner-fleet/internal/miners"
	"github.com/yorgof/miner-fleet/internal/store"
	"github.com/yorgof/miner-fleet/internal/web"
)

func TestCYDRegistrationPollingHistoryAndUI(t *testing.T) {
	t.Setenv("MINER_FLEET_CYD_PASSWORD", "")
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/status" {
			t.Errorf("unexpected device request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"version":"v1.0.0","uptime":87444,"hashrate":1061802.300073843,"shares_ok":17365,"shares_bad":2,"best_diff":26.006313576209237,"pool_diff":0.0011582019686152392,"pool_connected":false,"rssi":-31,"settings":{"pool_host":"192.0.2.3"}}`)
	}))
	defer device.Close()
	u, _ := url.Parse(device.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := miners.NewRegistry(st, alert.NewNotifier(alert.Config{}, st), time.Hour)
	if err := reg.Start(ctx); err != nil {
		t.Fatal(err)
	}
	srv, err := web.NewServer(reg, st, templatesFS, staticFS)
	if err != nil {
		t.Fatal(err)
	}
	routes := srv.Routes()
	addForm := httptest.NewRecorder()
	routes.ServeHTTP(addForm, httptest.NewRequest("GET", "/miners/new", nil))
	if addForm.Code != 200 || !strings.Contains(addForm.Body.String(), `value="braiins"`) {
		t.Fatal("Braiins miner option missing")
	}

	form := url.Values{"name": {"CYD"}, "kind": {"cyd"}, "host": {u.Hostname()}, "port": {u.Port()}}
	req := httptest.NewRequest("POST", "/miners", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reply := httptest.NewRecorder()
	routes.ServeHTTP(reply, req)
	if reply.Code != http.StatusSeeOther {
		t.Fatalf("registration: %d %s", reply.Code, reply.Body)
	}
	ms, err := st.ListMiners(ctx)
	if err != nil || len(ms) != 1 {
		t.Fatalf("miners: %v %v", ms, err)
	}
	id := ms[0].ID
	deadline := time.Now().Add(3 * time.Second)
	for {
		sample, err := st.LatestSample(ctx, id)
		if err == nil && sample.Ok {
			if sample.HashrateGHs != 1061802.300073843/1e9 || sample.SharesAccepted != 17365 || sample.SharesRejected != 2 || sample.UptimeS != 87444 || sample.WifiRSSI != -31 || sample.AutoFanMode != -1 {
				t.Fatalf("wrong normalized sample: %+v", sample)
			}
			if !strings.Contains(sample.RawJSON, `"pool_connected":false`) {
				t.Fatal("raw status not stored")
			}
			sample.TS = sample.TS.Add(-time.Minute)
			if err := st.InsertSample(ctx, sample); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no successful poll: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, path := range []string{"/", "/miners/" + strconv.FormatInt(id, 10), "/partials/miner/" + strconv.FormatInt(id, 10)} {
		reply := httptest.NewRecorder()
		routes.ServeHTTP(reply, httptest.NewRequest("GET", path, nil))
		body := reply.Body.String()
		if reply.Code != 200 || !strings.Contains(body, "1.1 MH/s") || !strings.Contains(body, "offline") || !strings.Contains(body, "17365") {
			t.Fatalf("%s rendered wrong: %d %s", path, reply.Code, body)
		}
		if strings.Contains(body, "Temp") || strings.Contains(body, "Power</") || strings.Contains(body, "Fan %") {
			t.Fatalf("CYD shows unavailable sensor/control: %s", body)
		}
		if path != "/" && (!strings.Contains(body, "1.1 MH/s</span>") || !strings.Contains(body, "0.0011582")) {
			t.Fatalf("CYD chart, pool difficulty or restart missing: %s", body)
		}
		if strings.HasPrefix(path, "/miners/") && !strings.Contains(body, "Restart") {
			t.Fatal("restart control missing from details")
		}
		if strings.HasPrefix(path, "/partials/") && strings.Contains(body, "action-form") {
			t.Fatal("polling fragment must not replace control forms")
		}
	}
	client, _ := reg.ClientFor(id)
	if err := client.SetFan(ctx, 50); err == nil {
		t.Fatal("CYD fan write accepted")
	}

	form.Set("host", "cyd.local")
	req = httptest.NewRequest("POST", "/miners", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reply = httptest.NewRecorder()
	routes.ServeHTTP(reply, req)
	if reply.Code != 400 {
		t.Fatalf("hostname accepted: %d", reply.Code)
	}
	ms, _ = st.ListMiners(ctx)
	if len(ms) != 1 {
		t.Fatal("invalid miner stored")
	}
}

func TestFleetPrivateCredentialsProfilesAndRequestProtection(t *testing.T) {
	t.Setenv("MINER_FLEET_PASSWORD", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := miners.NewRegistry(st, alert.NewNotifier(alert.Config{}, st), time.Hour)
	if err = reg.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateMiner(ctx, "heater", "braiins", "192.0.2.20", 4028)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.NewServer(reg, st, templatesFS, staticFS)
	if err != nil {
		t.Fatal(err)
	}
	routes := srv.Routes()
	post := func(path string, form url.Values, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		reply := httptest.NewRecorder()
		routes.ServeHTTP(reply, req)
		return reply
	}
	path := "/miners/" + strconv.FormatInt(id, 10)
	denied := post(path+"/credentials", url.Values{"password": {"fixture-password"}}, "https://untrusted.example")
	if denied.Code != 403 {
		t.Fatal("cross-site write allowed", denied.Code)
	}
	// Disable polling before saving credentials so this test makes no external network calls.
	m, _ := st.GetMiner(ctx, id)
	m.Enabled = false
	if err = st.UpdateMiner(ctx, m); err != nil {
		t.Fatal(err)
	}
	reply := post(path+"/credentials", url.Values{"username": {"root"}, "password": {"fixture-password"}, "web_port": {"80"}}, "")
	if reply.Code != 200 || strings.Contains(reply.Body.String(), "fixture-password") {
		t.Fatal("credential save failed/leaked", reply.Code, reply.Body)
	}
	reply = post(path+"/control/autotuning", url.Values{"powerTarget": {"750"}, "save_profile": {"quiet"}}, "")
	if reply.Code != 200 {
		t.Fatal("profile save failed", reply.Code, reply.Body)
	}
	profiles, err := st.Profiles(ctx, id)
	if err != nil || len(profiles) != 1 {
		t.Fatal("profile missing", profiles, err)
	}
	req := httptest.NewRequest("GET", "/fleet/export", nil)
	reply = httptest.NewRecorder()
	routes.ServeHTTP(reply, req)
	if reply.Code != 200 || strings.Contains(reply.Body.String(), "fixture-password") || strings.Contains(reply.Body.String(), "ciphertext") || !strings.Contains(reply.Body.String(), "quiet") {
		t.Fatal("unsafe export", reply.Code, reply.Body)
	}
	reply = post("/fleet/schedules", url.Values{"name": {"night"}, "miner_id": {strconv.FormatInt(id, 10)}, "profile_id": {strconv.FormatInt(profiles[0].ID, 10)}, "operation": {"pause"}, "timezone": {"UTC"}, "days": {"1"}, "at": {"22:00"}}, "")
	if reply.Code != 200 {
		t.Fatal("schedule save", reply.Code, reply.Body)
	}
	schedules, _ := st.Schedules(ctx)
	if len(schedules) != 1 || schedules[0].Enabled {
		t.Fatal("schedule should start disabled", schedules)
	}
	reply = post("/fleet/schedules/"+strconv.FormatInt(schedules[0].ID, 10)+"/toggle", url.Values{"enabled": {"1"}}, "")
	if reply.Code != 200 {
		t.Fatal("schedule toggle failed", reply.Code, reply.Body)
	}
	schedules, _ = st.Schedules(ctx)
	if !schedules[0].Enabled {
		t.Fatal("schedule not enabled")
	}
	for _, p := range []string{path, "/fleet", "/partials/miner/" + strconv.FormatInt(id, 10)} {
		reply = httptest.NewRecorder()
		routes.ServeHTTP(reply, httptest.NewRequest("GET", p, nil))
		if reply.Code != 200 || strings.Contains(reply.Body.String(), "fixture-password") {
			t.Fatal("page failed or leaked credentials", p, reply.Code)
		}
		if strings.HasPrefix(p, "/partials/") && strings.Contains(reply.Body.String(), "action-form") {
			t.Fatal("refresh replaces forms")
		}
	}
	t.Setenv("MINER_FLEET_PASSWORD", "fixture-dashboard-password")
	reply = httptest.NewRecorder()
	routes.ServeHTTP(reply, httptest.NewRequest("GET", "/fleet", nil))
	if reply.Code != 401 {
		t.Fatal("optional login not enforced")
	}
	req = httptest.NewRequest("GET", "/fleet", nil)
	req.SetBasicAuth("admin", "fixture-dashboard-password")
	reply = httptest.NewRecorder()
	routes.ServeHTTP(reply, req)
	if reply.Code != 200 {
		t.Fatal("valid dashboard login rejected")
	}
	reply = httptest.NewRecorder()
	routes.ServeHTTP(reply, httptest.NewRequest("GET", "/healthz", nil))
	if reply.Code != 200 {
		t.Fatal("container healthcheck requires login")
	}
}
