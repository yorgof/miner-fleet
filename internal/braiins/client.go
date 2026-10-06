// Package braiins monitors BraiinsOS+ through its BOSminer socket API.
package braiins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/yorgof/miner-fleet/internal/sanitize"
)

type Client struct{ addr string }

func New(host string, port int) *Client {
	if port == 0 {
		port = 4028
	}
	return &Client{addr: net.JoinHostPort(host, strconv.Itoa(port))}
}

type Stats struct {
	HashrateGHs    float64
	TempC          float64
	FanRPM         int
	FanPercent     float64
	PowerW         float64 // BOSminer estimate, not a wall-meter reading
	SharesAccepted int64
	SharesRejected int64
	UptimeS        int64
	BestDiff       float64
	BlocksFound    int64
	Raw            string
}

func (c *Client) FetchStats(ctx context.Context) (Stats, error) {
	summary, body, err := c.command(ctx, "summary", "SUMMARY")
	if err != nil {
		return Stats{}, err
	}
	if len(summary) == 0 {
		return Stats{}, fmt.Errorf("BOSminer summary has no entries")
	}
	s := summary[0]
	rate, ok := number(s, "MHS 5s")
	if !ok {
		rate, ok = number(s, "MHS av")
	}
	if !ok {
		return Stats{}, fmt.Errorf("BOSminer summary is missing hashrate")
	}
	stats := Stats{HashrateGHs: rate / 1000}
	v, _ := number(s, "Accepted")
	stats.SharesAccepted = int64(v)
	v, _ = number(s, "Rejected")
	stats.SharesRejected = int64(v)
	v, _ = number(s, "Elapsed")
	stats.UptimeS = int64(v)
	v, _ = number(s, "Found Blocks")
	stats.BlocksFound = int64(v)
	stats.BestDiff, _ = number(s, "Best Share")
	raw := map[string]json.RawMessage{"summary": body}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, cmd := range []struct{ command, field string }{{"devs", "DEVS"}, {"devdetails", "DEVDETAILS"}, {"pools", "POOLS"}, {"tempctrl", "TEMPCTRL"}, {"version", "VERSION"}, {"coin", "COIN"}, {"stats", "STATS"}, {"config", "CONFIG"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, b, e := c.command(ctx, cmd.command, cmd.field); e == nil {
				mu.Lock()
				raw[cmd.command] = b
				mu.Unlock()
			}
		}()
	}

	// Older firmware can omit an optional command; retain working summary data.
	if entries, body, err := c.command(ctx, "temps", "TEMPS"); err == nil {
		mu.Lock()
		raw["temps"] = body
		mu.Unlock()
		for _, entry := range entries {
			temp, ok := number(entry, "Chip")
			if !ok || temp <= 0 {
				temp, _ = number(entry, "Board")
			}
			stats.TempC = max(stats.TempC, temp)
		}
	}
	if entries, body, err := c.command(ctx, "fans", "FANS"); err == nil {
		mu.Lock()
		raw["fans"] = body
		mu.Unlock()
		for _, entry := range entries {
			rpm, _ := number(entry, "RPM")
			speed, _ := number(entry, "Speed")
			stats.FanRPM = max(stats.FanRPM, int(rpm))
			stats.FanPercent = max(stats.FanPercent, speed)
		}
	}
	if entries, body, err := c.command(ctx, "tunerstatus", "TUNERSTATUS"); err == nil {
		mu.Lock()
		raw["tunerstatus"] = body
		mu.Unlock()
		if len(entries) > 0 {
			stats.PowerW, _ = number(entries[0], "ApproximateMinerPowerConsumption")
		}
	}
	wg.Wait()
	encoded, err := json.Marshal(raw)
	if err != nil {
		return Stats{}, err
	}
	stats.Raw = sanitize.JSON(string(encoded))
	return stats, nil
}

// Control sends only the two documented socket write operations. A status
// response is checked even when the operation has no result array.
func (c *Client) Control(ctx context.Context, operation string) error {
	if operation != "pause" && operation != "resume" {
		return fmt.Errorf("unsupported socket control")
	}
	_, _, err := c.command(ctx, operation, "")
	return err
}

func number(entry map[string]json.RawMessage, key string) (float64, bool) {
	value, ok := entry[key]
	if !ok || bytes.Equal(value, []byte("null")) {
		return 0, false
	}
	var n float64
	if json.Unmarshal(value, &n) == nil {
		return n, true
	}
	var s string
	if json.Unmarshal(value, &s) == nil {
		n, err := strconv.ParseFloat(s, 64)
		return n, err == nil
	}
	return 0, false
}

func (c *Client) command(ctx context.Context, command, field string) ([]map[string]json.RawMessage, json.RawMessage, error) {
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, nil, fmt.Errorf("BOSminer %s: %w", command, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}
	if err := json.NewEncoder(conn).Encode(map[string]string{"command": command}); err != nil {
		return nil, nil, err
	}
	var body bytes.Buffer
	chunk := make([]byte, 4096)
	for {
		n, err := conn.Read(chunk)
		if end := bytes.IndexByte(chunk[:n], 0); end >= 0 {
			body.Write(chunk[:end])
			break
		}
		body.Write(chunk[:n])
		if body.Len() > 1<<20 {
			return nil, nil, fmt.Errorf("BOSminer %s response exceeds 1 MiB", command)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read BOSminer %s: %w", command, err)
		}
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body.Bytes(), &response); err != nil {
		return nil, nil, fmt.Errorf("decode BOSminer %s: %w", command, err)
	}
	var status []struct {
		Status string `json:"STATUS"`
	}
	if err := json.Unmarshal(response["STATUS"], &status); err != nil || len(status) == 0 {
		return nil, nil, fmt.Errorf("BOSminer %s: missing status", command)
	}
	if status[0].Status != "S" {
		return nil, nil, fmt.Errorf("BOSminer %s failed (status %s)", command, status[0].Status)
	}
	if field == "" {
		return nil, append(json.RawMessage(nil), body.Bytes()...), nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(response[field], &entries); err != nil {
		return nil, nil, fmt.Errorf("decode BOSminer %s entries: %w", command, err)
	}
	return entries, append(json.RawMessage(nil), body.Bytes()...), nil
}
