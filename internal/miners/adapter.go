// Package miners normalizes AxeOS/CYD HTTP and Avalon/BOSminer TCP
// behind one Client interface and owns the poll loop that keeps
// SQLite and the in-memory "latest stats" cache up to date.
package miners

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/yorgof/miner-fleet/internal/sanitize"
	"net"
	"os"
	"strings"
	"time"

	"github.com/yorgof/miner-fleet/internal/avalon"
	"github.com/yorgof/miner-fleet/internal/axeos"
	"github.com/yorgof/miner-fleet/internal/braiins"
	"github.com/yorgof/miner-fleet/internal/cyd"
)

const (
	KindAxeOS         = "axeos"
	KindAvalonCGMiner = "avalon_cgminer"
	KindCYD           = "cyd"
	KindBraiins       = "braiins"
)

// Stats is the normalized shape every adapter reports into, regardless of
// wire format. Ok=false means the last poll failed; the numeric fields are
// then zero and Error explains why.
type Stats struct {
	Ok              bool
	Error           string
	HashrateGHs     float64
	TempC           float64
	VRTempC         float64
	PowerW          float64
	VoltageMV       float64
	Fan2RPM         int
	Fan2Percent     float64
	FrequencyMHz    float64
	CoreVoltageMV   float64
	ExpectedGHs     float64
	FanRPM          int
	FanPercent      float64
	AutoFanMode     int // 0 observed as manual/off; nonzero (1, 2, ...) as some auto mode. -1 = unknown/unsupported.
	SharesAccepted  int64
	SharesRejected  int64
	BestDiff        float64
	BestSessionDiff float64
	BlocksFound     int64
	UptimeS         int64
	WifiRSSI        int
	RawJSON         string
	SupportsRestart bool
	SupportsFan     bool
	PoolConnected   bool
	PoolHost        string
	PoolDiff        float64
	Version         string
	Model           string
	PoolKnown       bool
	Paused          bool
	SampledAt       time.Time
	LastSuccess     time.Time
	RejectPercent   float64
	Efficiency      float64
}

// Enrich normalizes optional fields without dropping firmware-specific data.
func Enrich(s *Stats, kind string) {
	normalized, _ := json.Marshal(Data(s.RawJSON))
	s.RawJSON = sanitize.JSON(string(normalized))
	var d map[string]any
	_ = json.Unmarshal([]byte(s.RawJSON), &d)
	str := func(k string) string { v, _ := d[k].(string); return v }
	flag := func(k string) bool { v, _ := d[k].(bool); return v }
	if kind == KindAxeOS {
		s.Version = str("version")
		s.Model = str("deviceModel")
		if s.Model == "" {
			s.Model = str("ASICModel")
		}
		s.PoolHost = str("stratumURL")
		s.Fan2Percent, _ = d["fanspeed2"].(float64)
		if n, ok := d["fanrpm2"].(float64); ok {
			s.Fan2RPM = int(n)
		}
		s.FrequencyMHz, _ = d["frequency"].(float64)
		s.CoreVoltageMV, _ = d["coreVoltage"].(float64)
		s.ExpectedGHs, _ = d["expectedHashrate"].(float64)
		s.Paused = flag("miningPaused") || flag("shutdown")
		if p, ok := d["poolConnectionInfo"].(map[string]any); ok {
			for _, k := range []string{"connected", "isConnected"} {
				if v, ok := p[k].(bool); ok {
					s.PoolKnown = true
					s.PoolConnected = v
				}
			}
		}
		if n, ok := d["poolDifficulty"].(float64); ok {
			s.PoolDiff = n
		}
	} else if kind == KindCYD {
		s.Model = "CYD ESP32"
		s.PoolKnown = true
	} else if kind == KindBraiins {
		s.Model = "Antminer / Braiins OS"
		if p, ok := d["pools"].(map[string]any); ok {
			if a, ok := p["POOLS"].([]any); ok {
				s.PoolKnown = true
				for _, v := range a {
					if p, ok := v.(map[string]any); ok {
						status, _ := p["Status"].(string)
						s.PoolConnected = s.PoolConnected || strings.EqualFold(status, "alive")
						if s.PoolHost == "" {
							s.PoolHost, _ = p["URL"].(string)
						}
					}
				}
			}
		}
	}
	if s.SharesAccepted+s.SharesRejected > 0 {
		s.RejectPercent = 100 * float64(s.SharesRejected) / float64(s.SharesAccepted+s.SharesRejected)
	}
	if s.PowerW > 0 && s.HashrateGHs > 0 {
		s.Efficiency = s.PowerW / (s.HashrateGHs / 1000)
	}
}

type Client interface {
	FetchStats(ctx context.Context) (Stats, error)
	Restart(ctx context.Context) error
	SetFan(ctx context.Context, percent int) error
	SetAutoFan(ctx context.Context, enabled bool) error
}

func NewClient(kind, host string, port int) (Client, error) {
	switch kind {
	case KindAxeOS:
		return &axeosAdapter{c: axeos.New(host, port)}, nil
	case KindAvalonCGMiner:
		return &avalonAdapter{c: avalon.New(host, port)}, nil
	case KindCYD:
		if ip := net.ParseIP(host); ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("CYD miners require the IPv4 address shown on the device")
		}
		return &cydAdapter{c: cyd.New(host, port, os.Getenv("MINER_FLEET_CYD_PASSWORD"))}, nil
	case KindBraiins:
		return &braiinsAdapter{c: braiins.New(host, port)}, nil
	default:
		return nil, fmt.Errorf("unknown miner kind %q", kind)
	}
}

type braiinsAdapter struct{ c *braiins.Client }

func (a *braiinsAdapter) FetchStats(ctx context.Context) (Stats, error) {
	s, err := a.c.FetchStats(ctx)
	if err != nil {
		return Stats{}, err
	}
	return Stats{
		HashrateGHs:     s.HashrateGHs,
		TempC:           s.TempC,
		FanRPM:          s.FanRPM,
		FanPercent:      s.FanPercent,
		PowerW:          s.PowerW,
		AutoFanMode:     -1,
		SharesAccepted:  s.SharesAccepted,
		SharesRejected:  s.SharesRejected,
		UptimeS:         s.UptimeS,
		BestDiff:        s.BestDiff,
		BestSessionDiff: s.BestDiff,
		BlocksFound:     s.BlocksFound,
		RawJSON:         s.Raw,
	}, nil
}

func (a *braiinsAdapter) Restart(context.Context) error {
	return fmt.Errorf("BraiinsOS+ monitoring adapter does not support restart")
}
func (a *braiinsAdapter) SetFan(context.Context, int) error {
	return fmt.Errorf("BraiinsOS+ monitoring adapter does not support fan controls")
}
func (a *braiinsAdapter) SetAutoFan(context.Context, bool) error {
	return fmt.Errorf("BraiinsOS+ monitoring adapter does not support fan controls")
}

type cydAdapter struct{ c *cyd.Client }

func (a *cydAdapter) FetchStats(ctx context.Context) (Stats, error) {
	s, raw, err := a.c.FetchStatus(ctx)
	if err != nil {
		return Stats{}, err
	}
	return Stats{
		HashrateGHs:     s.Hashrate / 1e9,
		AutoFanMode:     -1,
		SharesAccepted:  s.SharesOK,
		SharesRejected:  s.SharesBad,
		BestDiff:        s.BestDiff,
		BestSessionDiff: s.BestDiff,
		UptimeS:         s.Uptime,
		WifiRSSI:        s.RSSI,
		RawJSON:         raw,
		SupportsRestart: true,
		PoolConnected:   s.PoolConnected,
		PoolHost:        s.Settings.PoolHost,
		PoolDiff:        s.PoolDiff,
		Version:         s.Version,
	}, nil
}

func (a *cydAdapter) Restart(ctx context.Context) error { return a.c.Restart(ctx) }
func (a *cydAdapter) SetFan(context.Context, int) error {
	return fmt.Errorf("CYD miners do not have fan controls")
}
func (a *cydAdapter) SetAutoFan(context.Context, bool) error {
	return fmt.Errorf("CYD miners do not have fan controls")
}

type axeosAdapter struct{ c *axeos.Client }

func (a *axeosAdapter) FetchStats(ctx context.Context) (Stats, error) {
	s, err := a.c.FetchStats(ctx)
	if err != nil {
		return Stats{}, err
	}
	return Stats{
		HashrateGHs:     s.Info.HashRate,
		TempC:           s.Info.Temp,
		VRTempC:         s.Info.VRTemp,
		PowerW:          s.Info.Power,
		VoltageMV:       s.Info.Voltage,
		FanRPM:          s.Info.FanRPM,
		FanPercent:      s.Info.FanSpeed,
		AutoFanMode:     s.Info.AutoFanSpeed,
		SharesAccepted:  s.Info.SharesAccepted,
		SharesRejected:  s.Info.SharesRejected,
		BestDiff:        s.Info.BestDiff,
		BestSessionDiff: s.Info.BestSessionDiff,
		BlocksFound:     max(s.Info.BlockFound, s.Info.TotalFoundBlocks),
		UptimeS:         s.Info.UptimeSeconds,
		WifiRSSI:        s.Info.WifiRSSI,
		RawJSON:         s.Raw,
		SupportsRestart: true,
		SupportsFan:     true,
	}, nil
}

func (a *axeosAdapter) Restart(ctx context.Context) error { return a.c.Restart(ctx) }
func (a *axeosAdapter) SetFan(ctx context.Context, percent int) error {
	return a.c.SetFan(ctx, percent)
}
func (a *axeosAdapter) SetAutoFan(ctx context.Context, enabled bool) error {
	return a.c.SetAutoFan(ctx, enabled)
}

type avalonAdapter struct{ c *avalon.Client }

func (a *avalonAdapter) FetchStats(ctx context.Context) (Stats, error) {
	s, err := a.c.FetchStats(ctx)
	if err != nil {
		return Stats{}, err
	}
	return Stats{
		HashrateGHs:     s.HashrateGHs,
		TempC:           s.TempC,
		FanRPM:          s.FanRPM,
		FanPercent:      s.FanPercent,
		AutoFanMode:     -1, // not exposed over the cgminer API
		SharesAccepted:  s.SharesAccepted,
		SharesRejected:  s.SharesRejected,
		BlocksFound:     s.BlocksFound,
		UptimeS:         s.UptimeS,
		RawJSON:         s.Raw,
		SupportsRestart: false,
		SupportsFan:     false,
	}, nil
}

func (a *avalonAdapter) Restart(ctx context.Context) error { return a.c.Restart(ctx) }
func (a *avalonAdapter) SetFan(ctx context.Context, percent int) error {
	return a.c.SetFan(ctx, percent)
}
func (a *avalonAdapter) SetAutoFan(ctx context.Context, enabled bool) error {
	return a.c.SetAutoFan(ctx, enabled)
}
