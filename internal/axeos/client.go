// Package axeos talks to the AxeOS REST API shared by Bitaxe and NerdQAxe
// firmware. Stats.Raw preserves fields that are not normalized, including
// stratum configuration, per-fan PID curves and tuning settings.
package axeos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(host string, port int) *Client {
	if port == 0 {
		port = 80
	}
	return &Client{
		baseURL: "http://" + net.JoinHostPort(host, strconv.Itoa(port)),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// systemInfo mirrors the known fields of GET /api/system/info. Unknown
// fields are not lost - callers should also keep the raw body.
type systemInfo struct {
	Hostname  string  `json:"hostname"`
	ASICModel string  `json:"ASICModel"`
	HashRate  float64 `json:"hashRate"` // GH/s
	Temp      float64 `json:"temp"`
	VRTemp    float64 `json:"vrTemp"`
	Power     float64 `json:"power"`
	Voltage   float64 `json:"voltage"`
	FanRPM    int     `json:"fanrpm"`
	// fanspeed is a percent, but a real device reports it as a float (e.g.
	// 99.418...), not a whole number - an int field here made every single
	// real device fail to decode entirely (Go's json package rejects a
	// fractional number into an int field), caught 2026-08-24 against real
	// hardware. autofanspeed is not a plain bool either: observed values are
	// 1 (Bitaxe) and 2 (NerdQAxe++), presumably distinct auto-fan modes, so
	// it stays an int - treat any nonzero value as "auto".
	FanSpeed       float64 `json:"fanspeed"`
	AutoFanSpeed   int     `json:"autofanspeed"`
	SharesAccepted int64   `json:"sharesAccepted"`
	SharesRejected int64   `json:"sharesRejected"`
	// bestDiff/bestSessionDiff are plain difficulty numbers, not
	// pre-formatted "4.53G" strings - format for display, don't assume one.
	BestDiff        float64 `json:"bestDiff"`
	BestSessionDiff float64 `json:"bestSessionDiff"`
	UptimeSeconds   int64   `json:"uptimeSeconds"`
	WifiRSSI        int     `json:"wifiRSSI"`
	StratumURL      string  `json:"stratumURL"`
	// The block-found counter uses a different key depending on firmware
	// fork: plain Bitaxe firmware uses "blockFound", NerdQAxe++ uses
	// "totalFoundBlocks" instead (and doesn't have "blockFound" at all) -
	// confirmed against both real devices 2026-08-24. Both are read; callers
	// should take whichever is nonzero.
	BlockFound       int64 `json:"blockFound"`
	TotalFoundBlocks int64 `json:"totalFoundBlocks"`
}

type Stats struct {
	Info systemInfo
	Raw  string
}

func (c *Client) FetchStats(ctx context.Context) (Stats, error) {
	body, err := c.doGet(ctx, "/api/system/info")
	if err != nil {
		return Stats{}, err
	}
	var info systemInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return Stats{}, fmt.Errorf("decode /api/system/info: %w", err)
	}
	return Stats{Info: info, Raw: string(body)}, nil
}

func (c *Client) Restart(ctx context.Context) error {
	_, err := c.doPost(ctx, "/api/system/restart", nil)
	return err
}

func (c *Client) SetFan(ctx context.Context, percent int) error {
	_, err := c.doPatch(ctx, "/api/system", map[string]any{"manualFanSpeed": percent})
	return err
}

func (c *Client) SetAutoFan(ctx context.Context, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := c.doPatch(ctx, "/api/system", map[string]any{"autofanspeed": v})
	return err
}

func (c *Client) doGet(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) doPost(ctx context.Context, path string, payload any) ([]byte, error) {
	return c.doWithBody(ctx, http.MethodPost, path, payload)
}

func (c *Client) doPatch(ctx context.Context, path string, payload any) ([]byte, error) {
	return c.doWithBody(ctx, http.MethodPatch, path, payload)
}

func (c *Client) doWithBody(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return b, fmt.Errorf("%s %s: HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, string(b))
	}
	return b, nil
}
