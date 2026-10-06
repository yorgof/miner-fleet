package miners

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"math"
	"github.com/yorgof/miner-fleet/internal/alert"
	"github.com/yorgof/miner-fleet/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNerdControlsUseInstalledFirmwareContract(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	called := false
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != "PATCH" || r.URL.Path != "/api/system" || r.Header.Get("X-OTP-Session") != "fixture-session" {
			t.Errorf("incorrect request: %s %s", r.Method, r.URL.Path)
		}
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		if v["manualFanSpeed"] != float64(35) || v["autofanspeed"] != float64(0) || v["fanspeed"] != nil {
			t.Errorf("wrong Nerd fan fields: %+v", v)
		}
		w.WriteHeader(204)
	}))
	defer device.Close()
	u, _ := url.Parse(device.URL)
	port, _ := strconv.Atoi(u.Port())
	id, err := st.CreateMiner(ctx, "nerd", KindAxeOS, u.Hostname(), port)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(st, alert.NewNotifier(alert.Config{}, st), time.Hour)
	reg.latest[id] = Stats{Ok: true, RawJSON: `{"deviceModel":"NerdQAxe++","fans":[],"version":"v1.0.37.3-LTS"}`}
	if err = st.SaveCredentials(ctx, id, store.Credentials{SessionToken: "fixture-session"}); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(st, reg)
	if _, err = mgr.Execute(ctx, id, "cooling", map[string]any{"manualFanSpeed": float64(35), "autofanspeed": float64(0)}, nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("control never reached device")
	}
	for _, unsupported := range []string{"identify", "pause", "resume"} {
		if _, err = mgr.Execute(ctx, id, unsupported, map[string]any{}, nil); err == nil {
			t.Fatal("unsupported Nerd endpoint accepted", unsupported)
		}
	}
	events, err := st.Events(ctx, id)
	if err != nil || len(events) != 1 || strings.Contains(events[0].Detail, "fixture-session") {
		t.Fatal("incorrect audit", events, err)
	}
}
func TestNormalizePreservesAvalonPackedResponsesAndRedacts(t *testing.T) {
	s := Stats{RawJSON: "{\"STATS\":[{\"MM ID0\":\"TAvg[68]\",\"password\":\"fixture-secret\"}]}\n{\"SUMMARY\":[{\"MHS av\":400000}]}", PowerW: 100, HashrateGHs: 500}
	Enrich(&s, KindAvalonCGMiner)
	if strings.Contains(s.RawJSON, "fixture-secret") || !strings.Contains(s.RawJSON, "TAvg[68]") || !strings.Contains(s.RawJSON, "SUMMARY") || s.Efficiency != 200 {
		t.Fatalf("data lost or secret exposed: %+v", s)
	}
}
func TestControlValidationRejectsUnknownAndNonFinite(t *testing.T) {
	c := settings("cooling", "cooling", "", f("speed", "speed", "number", 0, 100))
	for _, v := range []map[string]any{{"speed": math.NaN()}, {"speed": math.Inf(1)}, {"speed": float64(101)}, {"shell": "echo unsafe"}, {}} {
		if Validate(c, v) == nil {
			t.Fatalf("bad value accepted: %+v", v)
		}
	}
}

func TestNerdLogDownloadUsesWebSocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ws" {
			t.Error("incorrect log endpoint")
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		c.WriteMessage(websocket.TextMessage, []byte("fixture log line\n"))
		c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	}))
	defer device.Close()
	u, _ := url.Parse(device.URL)
	port, _ := strconv.Atoi(u.Port())
	mgr := NewManager(nil, nil)
	got, err := mgr.nerdLogs(context.Background(), store.Miner{Kind: KindAxeOS, Host: u.Hostname(), Port: port}, store.Credentials{})
	if err != nil || string(got) != "fixture log line\n" {
		t.Fatal("log capture failed", string(got), err)
	}
}

func TestStructuredSettingsUseTheirDeclaredShape(t *testing.T) {
	c := settings("fan_channels", "fans", "", f("fans", "fans", "json", 0, 0))
	for _, v := range []any{map[string]any{}, []any{"invalid"}, []any{map[string]any{"mode": float64(1)}}, []any{map[string]any{"manualSpeed": float64(-1)}}} {
		if Validate(c, map[string]any{"fans": v}) == nil {
			t.Fatal("invalid fan configuration accepted", v)
		}
	}
	if err := Validate(c, map[string]any{"fans": []any{map[string]any{"mode": float64(2), "manualSpeed": float64(35)}}}); err != nil {
		t.Fatal(err)
	}
}
