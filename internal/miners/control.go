package miners

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yorgof/miner-fleet/internal/braiins"
	"github.com/yorgof/miner-fleet/internal/sanitize"
	"github.com/yorgof/miner-fleet/internal/store"
)

type Field struct {
	Key, Label, Type, Value string
	Min, Max                float64
	Options                 []string
	Required                bool
}
type Control struct {
	ID, Label, Help string
	Fields          []Field
	Auth            bool
	Upload          bool
	Download        bool
	Profile         bool
}
type Detail struct {
	Data        map[string]any
	Controls    []Control
	Credentials bool
	Note        string
}
type extra struct {
	at   time.Time
	data map[string]any
	note string
}
type Manager struct {
	Store    *store.Store
	Registry *Registry
	HTTP     *http.Client
	mu       sync.Mutex
	extras   map[int64]extra
	locks    map[int64]*sync.Mutex
}

func NewManager(st *store.Store, reg *Registry) *Manager {
	return &Manager{Store: st, Registry: reg, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, extras: map[int64]extra{}, locks: map[int64]*sync.Mutex{}}
}
func (m *Manager) lock(id int64) func() {
	m.mu.Lock()
	l := m.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}
func (m *Manager) Invalidate(id int64) { m.mu.Lock(); delete(m.extras, id); m.mu.Unlock() }
func DeviceURL(v store.Miner, cred store.Credentials) string {
	p := v.Port
	if v.Kind == KindBraiins || v.Kind == KindAvalonCGMiner {
		p = cred.WebPort
	}
	if p == 0 {
		p = 80
	}
	return "http://" + net.JoinHostPort(v.Host, strconv.Itoa(p))
}
func Data(raw string) map[string]any {
	out := map[string]any{}
	if json.Unmarshal([]byte(raw), &out) != nil {
		parts := strings.Split(raw, "\n")
		for i, p := range parts {
			var v any
			if json.Unmarshal([]byte(p), &v) == nil {
				out[fmt.Sprintf("response_%d", i+1)] = v
			}
		}
	}
	return out
}
func Nerd(d map[string]any) bool {
	if _, ok := d["fans"].([]any); ok {
		return true
	}
	model, _ := d["deviceModel"].(string)
	return strings.Contains(strings.ToLower(model), "nerd")
}

func (m *Manager) Describe(ctx context.Context, id int64, fetch bool) (Detail, error) {
	miner, err := m.Store.GetMiner(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	stats, _ := m.Registry.Latest(id)
	d := Data(stats.RawJSON)
	cred, err := m.Store.Credentials(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	out := Detail{Data: d, Credentials: cred.Password != "" || cred.SessionToken != ""}
	m.mu.Lock()
	cached, ok := m.extras[id]
	m.mu.Unlock()
	if fetch && stats.Ok && (!ok || time.Since(cached.at) > time.Minute) {
		cached = extra{at: time.Now(), data: map[string]any{}}
		if miner.Kind == KindBraiins {
			w := braiins.NewWeb(DeviceURL(miner, cred))
			if v, e := w.Version(ctx); e == nil {
				cached.data["firmware"] = v
			}
			if cred.Password != "" {
				if e := w.Login(ctx, cred.Username, cred.Password); e != nil {
					cached.note = e.Error()
				} else if v, e := w.Configuration(ctx); e != nil {
					cached.note = e.Error()
				} else {
					cached.data["configuration"] = sanitize.Value(v)
				}
			} else {
				cached.note = "Save the miner's web login to read and change persistent settings."
			}
		} else if miner.Kind == KindAxeOS {
			paths := []string{"/api/system/asic", "/api/system/scoreboard", "/api/system/statistics"}
			if Nerd(d) {
				paths = []string{"/api/system/asic", "/api/influx/info", "/api/alert/info", "/api/swarm/info", "/api/otp/status", "/api/system/OTA/github"}
			}
			for _, path := range paths {
				if b, e := m.request(ctx, miner, cred, http.MethodGet, path, nil, ""); e == nil {
					var v any
					if json.Unmarshal(b, &v) == nil {
						cached.data[strings.TrimPrefix(path, "/api/")] = sanitize.Value(v)
					}
				}
			}
		}
		m.mu.Lock()
		m.extras[id] = cached
		m.mu.Unlock()
	}
	for k, v := range cached.data {
		d[k] = v
	}
	out.Note = cached.note
	out.Controls = Controls(miner.Kind, d)
	return out, nil
}

func f(key, label, typ string, min, max float64) Field {
	return Field{Key: key, Label: label, Type: typ, Min: min, Max: max}
}
func choice(key, label string, options ...string) Field {
	return Field{Key: key, Label: label, Type: "select", Options: options}
}
func settings(id, label, help string, fields ...Field) Control {
	return Control{ID: id, Label: label, Help: help, Fields: fields, Profile: true}
}
func Controls(kind string, d map[string]any) []Control {
	out := []Control{}
	if kind == KindAxeOS {
		nerd := Nerd(d)
		out = append(out, Control{ID: "restart", Label: "Restart"})
		if !nerd {
			out = append(out, Control{ID: "identify", Label: "Identify device"})
		} else {
			out = append(out, Control{ID: "reset_stats", Label: "Reset session statistics"})
		}
		if !nerd {
			if _, ok := d["miningPaused"]; ok {
				out = append(out, Control{ID: "pause", Label: "Pause mining"}, Control{ID: "resume", Label: "Resume mining"})
			}
			out = append(out, Control{ID: "dismiss_block", Label: "Dismiss block notification"})
		} else {
			out = append(out, Control{ID: "logs", Label: "Download five-second log capture", Download: true}, Control{ID: "shutdown", Label: "Stop hashing", Help: "The miner remains powered. Use Restart to start mining again."})
		}
		out = append(out, settings("performance", "Mining performance", "Changes apply according to the device firmware; some settings need a restart.", f("frequency", "ASIC frequency (MHz)", "number", 1, 2000), f("coreVoltage", "ASIC core voltage (mV)", "number", 1, 2000)))
		if nerd {
			out = append(out, settings("fan_channels", "Individual fans / PID", "One entry per fan. mode: 0 manual, 2 PID, 3 linked. Configure manualSpeed, overheatTemp and pid.targetTemp/p/i/d.", f("fans", "Per-fan settings (JSON array)", "json", 0, 0)))
			out = append(out, settings("cooling", "Cooling", "Each fan can use manual, automatic or PID control.", choice("autofanspeed", "Global fan mode (0 manual, 2 PID)", "0", "2"), f("manualFanSpeed", "Manual fan speed (%)", "number", 0, 100), f("pidTargetTemp", "PID temperature target (°C)", "number", 1, 90), f("pidP", "PID P", "number", 0, 655), f("pidI", "PID I", "number", 0, 655), f("pidD", "PID D", "number", 0, 655), f("overheat_temp", "Overheat threshold (°C)", "number", 1, 100)))
			out = append(out, settings("display", "Display", "", choice("flipscreen", "Flip screen", "true", "false"), choice("invertscreen", "Invert screen", "true", "false"), choice("autoscreenoff", "Automatic screen off", "true", "false")))
			out = append(out, settings("influx", "InfluxDB reporting", "Optional telemetry integration configured on the miner.", choice("influxEnable", "Enabled", "true", "false"), f("influxURL", "Host", "text", 0, 0), f("influxPort", "Port", "number", 1, 65535), f("influxBucket", "Bucket", "text", 0, 0), f("influxOrg", "Organization", "text", 0, 0), f("influxPrefix", "Measurement prefix", "text", 0, 0), f("influxToken", "Access token", "password", 0, 0)), settings("alerts", "Device alerts", "Firmware notification settings.", f("alertDiscordWebhook", "Discord webhook", "password", 0, 0), choice("alertDiscordWatchdogEnable", "Watchdog alerts", "true", "false"), choice("alertDiscordBlockFoundEnable", "Block alerts", "true", "false"), choice("alertDiscordBestDiffEnable", "Difficulty alerts", "true", "false"), choice("showBlockFoundScreenEnable", "Block screen", "true", "false")))

		} else {
			out = append(out, settings("cooling", "Cooling", "", choice("autofanspeed", "Automatic fan", "1", "0"), f("manualFanSpeed", "Manual fan speed (%)", "number", 0, 100), f("temptarget", "Temperature target (°C)", "number", 35, 66), f("minFanSpeed", "Minimum fan speed (%)", "number", 0, 99)))
			out = append(out, settings("display", "Display", "", choice("rotation", "Rotation", "0", "90", "180", "270"), choice("invertscreen", "Invert screen", "0", "1"), f("displayTimeout", "Display timeout (minutes; -1 always on, 0 off)", "number", -1, 65535)))
		}
		if nerd {
			out = append(out, settings("advanced", "Advanced settings", "Firmware-specific electrical and work settings.", f("jobInterval", "Job interval (ms)", "number", 1, 65535), choice("invertfanpolarity", "Invert fan polarity", "true", "false")))
		} else {
			out = append(out, settings("advanced", "Advanced settings", "Settings exposed by the installed AxeOS firmware.", choice("overclockEnabled", "Allow overclocking", "true", "false"), f("display", "Display model", "text", 0, 0), f("displayOffset", "Display offset", "number", 0, 255), f("statsFrequency", "Statistics frequency", "number", 0, 65535), choice("overheat_mode", "Clear overheat state", "0")))
		}
		pool := []Field{f("stratumURL", "Primary pool host", "text", 0, 0), f("stratumPort", "Primary pool port", "number", 1, 65535), f("stratumUser", "Primary worker / payout", "text", 0, 0), f("stratumPassword", "Primary pool password (blank keeps current)", "password", 0, 0), f("fallbackStratumURL", "Backup pool host", "text", 0, 0), f("fallbackStratumPort", "Backup pool port", "number", 1, 65535), f("fallbackStratumUser", "Backup worker / payout", "text", 0, 0), f("fallbackStratumPassword", "Backup pool password (blank keeps current)", "password", 0, 0)}
		if nerd {
			pool = append(pool, choice("stratumTLS", "Primary TLS", "true", "false"), choice("fallbackStratumTLS", "Backup TLS", "true", "false"), choice("stratumProtocol", "Primary protocol (0 V1, 1 V2)", "0", "1"), choice("fallbackStratumProtocol", "Backup protocol", "0", "1"), f("stratumDifficulty", "Suggested difficulty", "number", 0, 4294967295), choice("stratumEnonceSubscribe", "Extranonce subscribe", "true", "false"), choice("fallbackStratumEnonceSubscribe", "Backup extranonce subscribe", "true", "false"), choice("stratum_keep", "Keepalive", "true", "false"))
		} else {
			pool = append(pool, choice("stratumTLS", "Primary TLS mode (firmware enum)", "0", "1", "2", "3"), choice("fallbackStratumTLS", "Backup TLS mode", "0", "1", "2", "3"), f("stratumCert", "Primary TLS certificate", "text", 0, 0), f("fallbackStratumCert", "Backup TLS certificate", "text", 0, 0), choice("stratumV2ChannelType", "V2 channel type", "extended", "standard"), f("stratumV2AuthorityPubkey", "V2 authority public key", "text", 0, 0), choice("fallbackStratumV2ChannelType", "Backup V2 channel type", "extended", "standard"), f("fallbackStratumV2AuthorityPubkey", "Backup V2 authority public key", "text", 0, 0), choice("stratumDecodeCoinbase", "Decode primary coinbase", "true", "false"), choice("fallbackStratumDecodeCoinbase", "Decode backup coinbase", "true", "false"), f("fallbackStratumSuggestedDifficulty", "Backup suggested difficulty", "number", 0, 65535), choice("useFallbackStratum", "Use fallback pool", "true", "false"), choice("stratumProtocol", "Primary protocol", "SV1", "SV2"), choice("fallbackStratumProtocol", "Backup protocol", "SV1", "SV2"), f("stratumSuggestedDifficulty", "Suggested difficulty", "number", 0, 65535), choice("stratumExtranonceSubscribe", "Extranonce subscribe", "true", "false"), choice("fallbackStratumExtranonceSubscribe", "Backup extranonce subscribe", "true", "false"))
		}
		out = append(out, settings("pools", "Pools", "Blank fields leave their current values unchanged.", pool...), settings("network", "Network", "Wi-Fi changes may disconnect the miner; update its address here if needed.", f("hostname", "Hostname", "text", 0, 0), f("ssid", "Wi-Fi SSID", "text", 0, 0), f("wifiPass", "Wi-Fi password (blank keeps current)", "password", 0, 0)), Control{ID: "firmware", Label: "Upload firmware (.bin)", Upload: true, Help: "Use the OTA image built for this exact board."}, Control{ID: "web_firmware", Label: "Upload web interface (www.bin)", Upload: true})
		if !nerd {
			out = append(out, Control{ID: "logs", Label: "Download logs", Download: true}, Control{ID: "wifi_scan", Label: "Scan Wi-Fi", Download: true})
		}
	} else if kind == KindCYD {
		out = append(out, Control{ID: "restart", Label: "Restart"}, settings("settings", "Settings", "Saving settings restarts the CYD. Blank fields keep existing values.", f("pool_host", "Pool host", "text", 0, 0), f("pool_port", "Pool port", "number", 1, 65535), f("address", "Payout address", "text", 0, 0), f("worker", "Worker name", "text", 0, 0), choice("currency", "Currency", "USD", "CAD", "EUR", "GBP", "AUD", "JPY", "CHF"), choice("flip", "Flip screen", "true", "false"), f("ssid", "Wi-Fi SSID", "text", 0, 0), f("wifi_pass", "Wi-Fi password", "password", 0, 0), f("admin_pass", "New web password", "password", 0, 0), choice("drop", "Remove web password", "false", "true")), Control{ID: "firmware", Label: "Upload CYD OTA firmware (.bin)", Upload: true}, Control{ID: "wifi_scan", Label: "Scan Wi-Fi", Download: true})
	} else if kind == KindBraiins {
		out = append(out, Control{ID: "pause", Label: "Pause mining"}, Control{ID: "resume", Label: "Resume mining"})
		out = append(out, settings("autotuning", "Autotuning / heater power", "Set a power or hashrate target. Changing targets can initiate tuning.", choice("mode", "Target mode", "POWER_TARGET", "HASHRATE_TARGET"), f("powerTarget", "Power target (W)", "number", 1, 3000), f("hashrateTarget", "Hashrate target (TH/s)", "number", 0.1, 30), f("performanceScaling", "Dynamic performance scaling (JSON)", "json", 0, 0), f("hashChains", "Per-board tuning enabled (JSON array)", "json", 0, 0)), settings("cooling", "Cooling", "Existing protection thresholds are preserved when left blank.", choice("mode", "Temperature control", "AUTO", "MANUAL", "DISABLED"), f("targetTemp", "Target temperature (°C)", "number", 1, 90), f("hotTemp", "Hot threshold (°C)", "number", 1, 100), f("dangerousTemp", "Shutdown threshold (°C)", "number", 1, 110), f("speed", "Manual fan speed (%)", "number", 0, 100), f("minFans", "Minimum fans", "number", 0, 4), choice("immersionModeEnabled", "Immersion mode (requires immersion cooling)", "true", "false")), settings("performance", "Board performance", "Manual tuning can replace automatic tuning. Voltage is board voltage in volts.", choice("asicBoost", "ASICBoost", "true", "false"), f("globalFrequency", "Frequency (MHz)", "number", 1, 1000), f("globalVoltage", "Board voltage (V)", "number", 1, 12), f("hashChains", "Per-board settings (JSON array)", "json", 0, 0)), settings("pool_add", "Add pool", "Use group and pool IDs shown in Configuration data.", f("group", "Group ID", "text", 0, 0), f("url", "Stratum URL", "text", 0, 0), f("user", "Worker / payout", "text", 0, 0), f("password", "Pool password", "password", 0, 0), choice("enabled", "Enabled", "true", "false")), settings("pool_update", "Update pool", "", f("group", "Group ID", "text", 0, 0), f("pool", "Pool ID", "text", 0, 0), f("url", "Stratum URL", "text", 0, 0), f("user", "Worker / payout", "text", 0, 0), f("password", "Pool password", "password", 0, 0), choice("enabled", "Enabled", "true", "false")))
		out = append(out, settings("pool_remove", "Remove pool", "", f("group", "Group ID", "text", 0, 0), f("pool", "Pool ID", "text", 0, 0)), settings("pool_move", "Reorder pool", "", f("group", "Group ID", "text", 0, 0), f("pool", "Pool ID", "text", 0, 0), f("offset", "Offset (-1 up, 1 down)", "number", -20, 20)), settings("group_add", "Add pool group", "", f("name", "Group name", "text", 0, 0), f("quota", "Quota", "number", 0, 100)), settings("group_remove", "Remove pool group", "", f("id", "Group ID", "text", 0, 0)), settings("groups", "Pool groups", "Replace the group configuration with a JSON array.", f("groups", "Groups (JSON array)", "json", 0, 0)), settings("hostname", "Hostname", "", f("hostname", "Hostname", "text", 0, 0)), settings("static_ip", "Static network address", "This can disconnect the miner. Update the address in Miner Fleet afterwards.", f("address", "IP address", "text", 0, 0), f("netmask", "Netmask", "text", 0, 0), f("gateway", "Gateway", "text", 0, 0), f("dnsServers", "DNS servers (JSON array)", "json", 0, 0)), Control{ID: "dhcp", Label: "Use DHCP"}, settings("password", "Change web password", "Save the new password under Connection credentials afterwards.", f("newPassword", "New password", "password", 0, 0)), settings("auto_upgrade", "Automatic firmware updates", "", choice("enable", "Enabled", "true", "false")), settings("identify", "Fault light / identify", "", choice("enable", "Light on", "true", "false")), Control{ID: "restart", Label: "Restart mining service"}, Control{ID: "reboot", Label: "Reboot device"}, Control{ID: "start", Label: "Start mining service"}, Control{ID: "stop", Label: "Stop mining service"}, Control{ID: "logs", Label: "Download BOSminer logs", Download: true})

		out = append(out, settings("group_ratio_add", "Add fixed-share pool group", "", f("name", "Group name", "text", 0, 0), f("ratio", "Fixed share ratio", "number", 0, 1)), settings("group_name", "Rename pool group", "", f("group", "Group ID", "text", 0, 0), f("name", "Name", "text", 0, 0)), settings("group_quota", "Set group quota", "", f("group", "Group ID", "text", 0, 0), f("quota", "Quota", "number", 0, 100)), settings("group_ratio", "Set group fixed share ratio", "", f("group", "Group ID", "text", 0, 0), f("ratio", "Fixed share ratio", "number", 0, 1)), settings("pool_clear_password", "Remove pool password", "", f("group", "Group ID", "text", 0, 0), f("pool", "Pool ID", "text", 0, 0)))
		for i := range out {
			if out[i].ID != "pause" && out[i].ID != "resume" {
				out[i].Auth = true
			}
		}
	}
	required := map[string][]string{
		"pool_add": {"group", "url", "user"}, "pool_update": {"group", "pool"}, "pool_remove": {"group", "pool"}, "pool_move": {"group", "pool", "offset"},
		"group_add": {"name"}, "group_ratio_add": {"name", "ratio"}, "group_remove": {"id"}, "group_name": {"group", "name"}, "group_quota": {"group", "quota"}, "group_ratio": {"group", "ratio"},
		"pool_clear_password": {"group", "pool"}, "password": {"newPassword"}, "hostname": {"hostname"}, "static_ip": {"address", "netmask", "gateway", "dnsServers"}, "auto_upgrade": {"enable"}, "fan_channels": {"fans"},
	}
	if kind == KindBraiins {
		required["identify"] = []string{"enable"}
	}
	for i := range out {
		for j := range out[i].Fields {
			for _, key := range required[out[i].ID] {
				if out[i].Fields[j].Key == key {
					out[i].Fields[j].Required = true
				}
			}
		}
		if out[i].ID == "pool_add" || out[i].ID == "group_add" || out[i].ID == "group_ratio_add" {
			out[i].Profile = false
		}
		if out[i].ID == "network" || out[i].ID == "static_ip" || out[i].ID == "password" || strings.HasSuffix(out[i].ID, "remove") || out[i].ID == "pool_move" || out[i].ID == "pool_clear_password" {
			out[i].Profile = false
		}
		for j := range out[i].Fields {
			field := &out[i].Fields[j]
			if field.Type == "password" {
				continue
			}
			source := d
			if kind == KindCYD {
				if nested, ok := d["settings"].(map[string]any); ok {
					source = nested
				}
			}
			if kind == KindBraiins {
				source = configurationFields(d, out[i].ID)
			}
			if out[i].ID == "influx" || out[i].ID == "alerts" {
				key := "influx/info"
				if out[i].ID == "alerts" {
					key = "alert/info"
				}
				if nested, ok := d[key].(map[string]any); ok {
					source = nested
				}
			}

			if v, ok := source[field.Key]; ok {
				if n, ok := v.(float64); ok && field.Type == "number" && (n < field.Min || n > field.Max) {
					continue
				}
				field.Value = displayValue(v)
			}
		}
	}
	return out
}
func displayValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func Validate(c Control, values map[string]any) error {
	allowed := map[string]Field{}
	for _, f := range c.Fields {
		allowed[f.Key] = f
	}
	for _, f := range c.Fields {
		if f.Required {
			if v, ok := values[f.Key]; !ok || v == "" {
				return fmt.Errorf("%s is required", f.Label)
			}
		}
	}
	for k, v := range values {
		f, ok := allowed[k]
		if !ok {
			return fmt.Errorf("unsupported field %s", k)
		}
		switch f.Type {
		case "number":
			n, ok := v.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < f.Min || n > f.Max {
				return fmt.Errorf("%s is outside the supported range", f.Label)
			}
		case "json":
			if k == "performanceScaling" {
				if _, ok := v.(map[string]any); !ok {
					return fmt.Errorf("%s must be a JSON object", f.Label)
				}
			} else {
				a, ok := v.([]any)
				if !ok {
					return fmt.Errorf("%s must be a JSON array", f.Label)
				}
				if k == "fans" {
					for _, entry := range a {
						fan, ok := entry.(map[string]any)
						if !ok {
							return fmt.Errorf("each fan must be a JSON object")
						}
						for _, key := range []string{"mode", "manualSpeed", "overheatTemp"} {
							if value, exists := fan[key]; exists {
								n, ok := value.(float64)
								if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 {
									return fmt.Errorf("invalid per-fan %s", key)
								}
								if key == "mode" && n != 0 && n != 2 && n != 3 {
									return fmt.Errorf("fan mode must be 0, 2 or 3")
								}
							}
						}
					}
				}
			}
		case "select":
			s := displayValue(v)
			found := false
			for _, o := range f.Options {
				found = found || s == o
			}
			if !found {
				return fmt.Errorf("invalid %s", f.Label)
			}
		default:
			s, ok := v.(string)
			if !ok || len(s) > 4096 || strings.ContainsRune(s, '\x00') {
				return fmt.Errorf("invalid %s", f.Label)
			}
		}
	}
	if len(c.Fields) > 0 && len(values) == 0 {
		return fmt.Errorf("enter at least one setting")
	}
	return nil
}
func (m *Manager) Execute(ctx context.Context, id int64, op string, v map[string]any, upload []byte) ([]byte, error) {
	unlock := m.lock(id)
	defer unlock()
	miner, err := m.Store.GetMiner(ctx, id)
	if err != nil {
		return nil, err
	}
	stats, _ := m.Registry.Latest(id)
	d := Data(stats.RawJSON)
	var control *Control
	for _, c := range Controls(miner.Kind, d) {
		if c.ID == op {
			c := c
			control = &c
			break
		}
	}
	if control == nil {
		return nil, fmt.Errorf("this firmware does not support that operation")
	}
	if err = Validate(*control, v); err != nil {
		return nil, err
	}
	cred, err := m.Store.Credentials(ctx, id)
	if err != nil {
		return nil, err
	}
	var result []byte
	if miner.Kind == KindBraiins {
		if op == "pause" || op == "resume" {
			err = braiins.New(miner.Host, miner.Port).Control(ctx, op)
		} else {
			w := braiins.NewWeb(DeviceURL(miner, cred))
			if err = w.Login(ctx, cred.Username, cred.Password); err == nil {
				if op == "logs" {
					var data map[string]any
					data, err = w.Query(ctx, `query {bos {log(target:BOSMINER)}}`, nil)
					result, _ = json.MarshalIndent(data, "", "  ")
				} else {
					err = w.Mutation(ctx, op, v)
				}
			}
		}
	} else if miner.Kind == KindAxeOS && Nerd(d) && op == "logs" {
		result, err = m.nerdLogs(ctx, miner, cred)
	} else {
		method, path := http.MethodPatch, "/api/system"
		var body []byte
		body, _ = json.Marshal(v)
		if miner.Kind == KindCYD {
			method, path = http.MethodPost, "/api/settings"
		}
		switch op {
		case "influx":
			path = "/api/influx"
		case "alerts":
			path = "/api/alert"
		case "restart", "identify", "pause", "resume", "shutdown":
			method = http.MethodPost
			path = "/api/system/" + op
			if miner.Kind == KindCYD {
				path = "/api/restart"
			}
			body = nil
		case "reset_stats":
			method, path, body = http.MethodPost, "/api/system/reset-stats", nil
		case "dismiss_block":
			method, path, body = http.MethodPost, "/api/system/blockFound/dismiss", nil
		case "logs":
			method, path, body = http.MethodGet, "/api/system/logs", nil
		case "wifi_scan":
			method, path, body = http.MethodGet, "/api/system/wifi/scan", nil
			if miner.Kind == KindCYD {
				path = "/api/scan"
			}
		case "firmware", "web_firmware":
			method, path = http.MethodPost, "/api/system/OTA"
			if op == "web_firmware" {
				path = "/api/system/OTAWWW"
			}
			if miner.Kind == KindCYD {
				path = "/api/update"
			}
			body = upload
			if len(body) < 512 || len(body) > 8<<20 {
				return nil, fmt.Errorf("upload a valid OTA binary, up to 8 MiB")
			}
			if op == "firmware" && body[0] != 0xe9 {
				return nil, fmt.Errorf("expected an ESP OTA image, not a factory image")
			}
		}
		contentType := "application/json"
		if control.Upload {
			contentType = "application/octet-stream"
		}
		result, err = m.request(ctx, miner, cred, method, path, body, contentType)
	}
	m.Invalidate(id)
	if err == nil {
		if op == "pause" || op == "resume" || op == "restart" || op == "reboot" || op == "start" || op == "stop" || op == "shutdown" {
			_ = m.Store.SaveSetting(ctx, fmt.Sprintf("paused:%d", id), op == "pause" || op == "stop" || op == "shutdown")
			_ = m.Store.SaveSetting(ctx, fmt.Sprintf("pause_at:%d", id), time.Now())
		}
		if miner.Kind == KindBraiins && op == "password" {
			if p, ok := v["newPassword"].(string); ok {
				cred.Password = p
				_ = m.Store.SaveCredentials(ctx, id, cred)
			}
		}
		if miner.Kind == KindCYD && op == "settings" {
			if p, ok := v["admin_pass"].(string); ok {
				cred.Password = p
				_ = m.Store.SaveCredentials(ctx, id, cred)
				_ = m.Registry.Refresh(ctx, id)
			}
			if drop, ok := v["drop"].(bool); ok && drop {
				_ = m.Store.ClearCredentials(ctx, id)
				_ = m.Registry.Refresh(ctx, id)
			}
		}
	}
	detail := "Completed"
	if err != nil {
		detail = err.Error()
	}
	b, _ := json.Marshal(sanitize.Value(v))
	if len(v) > 0 {
		detail += " · " + string(b)
	}
	if e := m.Store.Event(context.Background(), id, "control", op, detail, err == nil); e != nil {
		return result, fmt.Errorf("could not save control audit: %w", e)
	}
	return result, err
}
func (m *Manager) request(ctx context.Context, miner store.Miner, cred store.Credentials, method, path string, body []byte, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, DeviceURL(miner, cred)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if miner.Kind == KindCYD {
		p := cred.Password
		if p == "" {
			p = os.Getenv("MINER_FLEET_CYD_PASSWORD")
		}
		if p != "" {
			req.SetBasicAuth("miner-fleet", p)
		}
	}
	if cred.SessionToken != "" {
		req.Header.Set("X-OTP-Session", cred.SessionToken)
	}
	if cred.OTP != "" {
		req.Header.Set("X-TOTP", cred.OTP)
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("miner request failed or timed out")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 8<<20 {
		return nil, fmt.Errorf("miner response too large")
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("miner returned HTTP %d", resp.StatusCode)
	}
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("<!")) || bytes.HasPrefix(bytes.TrimSpace(b), []byte("<html")) {
		return nil, fmt.Errorf("the miner does not expose this endpoint")
	}
	return b, nil
}

func configurationFields(d map[string]any, op string) map[string]any {
	at := func(v any, keys ...string) map[string]any {
		for _, k := range keys {
			m, ok := v.(map[string]any)
			if !ok {
				return map[string]any{}
			}
			v = m[k]
		}
		m, _ := v.(map[string]any)
		if m == nil {
			return map[string]any{}
		}
		return m
	}
	cfg := at(d, "configuration", "bosminer", "config")
	out := map[string]any{}
	copyFrom := func(v map[string]any) {
		for k, value := range v {
			out[k] = value
		}
	}
	switch op {
	case "autotuning":
		copyFrom(at(cfg, "autotuning"))
		out["performanceScaling"] = cfg["performanceScaling"]
	case "cooling":
		copyFrom(at(cfg, "tempControl"))
		copyFrom(at(cfg, "fanControl"))
	case "performance":
		g := at(cfg, "hashChainGlobal")
		out["asicBoost"] = g["asicBoost"]
		out["globalFrequency"] = g["frequency"]
		out["globalVoltage"] = g["voltage"]
		out["hashChains"] = cfg["hashChains"]
	case "hostname":
		copyFrom(at(d, "configuration", "bos"))
	case "static_ip":
		copyFrom(at(d, "configuration", "bos", "network"))
	case "auto_upgrade":
		out["enable"] = at(d, "configuration", "bos")["autoUpgrade"]
	case "identify":
		out["enable"] = at(d, "configuration", "bos")["faultLight"]
	}
	return out
}

func (m *Manager) OTPSession(ctx context.Context, id int64, code string) (string, error) {
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return "", fmt.Errorf("enter a six-digit one-time code")
	}
	miner, err := m.Store.GetMiner(ctx, id)
	if err != nil {
		return "", err
	}
	if miner.Kind != KindAxeOS {
		return "", fmt.Errorf("OTP sessions require NerdQAxe firmware")
	}
	b, err := m.request(ctx, miner, store.Credentials{OTP: code}, http.MethodPost, "/api/otp/session", nil, "application/json")
	if err != nil {
		return "", err
	}
	var v struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return "", fmt.Errorf("invalid OTP session response")
	}
	return v.Token, nil
}

func WithMetadata(s Stats, d map[string]any) Stats {
	if f, ok := d["firmware"].(map[string]any); ok {
		if bos, ok := f["bos"].(map[string]any); ok {
			if info, ok := bos["info"].(map[string]any); ok {
				if version, ok := info["version"].(map[string]any); ok {
					s.Version, _ = version["full"].(string)
				}
			}
		}
		if b, ok := f["bosminer"].(map[string]any); ok {
			if info, ok := b["info"].(map[string]any); ok {
				if model, ok := info["modelName"].(string); ok {
					s.Model = model
				}
			}
		}
	}
	return s
}
