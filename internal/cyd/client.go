// Package cyd talks to cyd-miner's HTTP API. Hashrate is reported in H/s.
package cyd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL  string
	password string
	http     *http.Client
}

func New(host string, port int, password string) *Client {
	if port == 0 {
		port = 80
	}
	return &Client{
		baseURL:  fmt.Sprintf("http://%s:%d", host, port),
		password: password,
		http: &http.Client{Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

type Status struct {
	Version       string  `json:"version"`
	Uptime        int64   `json:"uptime"`
	Hashrate      float64 `json:"hashrate"`
	SharesOK      int64   `json:"shares_ok"`
	SharesBad     int64   `json:"shares_bad"`
	BestDiff      float64 `json:"best_diff"`
	PoolDiff      float64 `json:"pool_diff"`
	PoolConnected bool    `json:"pool_connected"`
	RSSI          int     `json:"rssi"`
	Settings      struct {
		PoolHost string `json:"pool_host"`
	} `json:"settings"`
}

func (c *Client) FetchStatus(ctx context.Context) (Status, string, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/status")
	if err != nil {
		return Status{}, "", err
	}
	var status Status
	if err := json.Unmarshal(body, &status); err != nil {
		return Status{}, "", fmt.Errorf("decode /api/status: %w", err)
	}
	// Reject an unrelated JSON endpoint instead of treating it as an online miner.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields["hashrate"] == nil || fields["shares_ok"] == nil || fields["pool_connected"] == nil {
		return Status{}, "", fmt.Errorf("/api/status is missing cyd-miner status fields")
	}
	return status, string(body), nil
}

func (c *Client) Restart(ctx context.Context) error {
	body, err := c.do(ctx, http.MethodPost, "/api/restart")
	if err != nil {
		return err
	}
	var reply struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &reply); err != nil || !reply.OK {
		return fmt.Errorf("/api/restart did not acknowledge the restart")
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.password != "" {
		req.SetBasicAuth("miner-fleet", c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("CYD password needed or incorrect; configure MINER_FLEET_CYD_PASSWORD on the server")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
