// Package avalon talks to the classic cgminer-family API that Canaan Avalon
// devices (including the Nano 3) expose on TCP port 4028: one JSON command
// per connection, e.g. {"command":"summary"}, response terminated by
// connection close plus a trailing NUL byte. Verified against a real Avalon
// Nano 3 on 2026-08-24.
//
// Restart and fan control are NOT implemented: no documented write command
// was found on the cgminer API (the classic API is read-oriented, and
// Avalon's own web UI - separate, session-authenticated - is the only place
// that appeared to expose settings). Both methods return ErrUnsupported so
// the web UI hides those controls rather than offering ones that would just
// fail or, worse, do something undocumented to live hardware.
package avalon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrUnsupported = errors.New("avalon: not supported over the cgminer API")

type Client struct {
	addr    string
	timeout time.Duration
}

func New(host string, port int) *Client {
	if port == 0 {
		port = 4028
	}
	return &Client{addr: net.JoinHostPort(host, strconv.Itoa(port)), timeout: 5 * time.Second}
}

type Stats struct {
	HashrateGHs    float64
	TempC          float64
	FanRPM         int
	FanPercent     float64
	SharesAccepted int64
	SharesRejected int64
	UptimeS        int64
	BlocksFound    int64
	Raw            string // both "summary" and "stats" responses, newline-joined
}

func (c *Client) FetchStats(ctx context.Context) (Stats, error) {
	summaryBody, err := c.command(ctx, "summary")
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{Raw: string(summaryBody)}

	var summary struct {
		SUMMARY []map[string]json.RawMessage `json:"SUMMARY"`
	}
	if err := json.Unmarshal(summaryBody, &summary); err != nil {
		return stats, fmt.Errorf("decode summary: %w", err)
	}
	if len(summary.SUMMARY) == 0 {
		return stats, fmt.Errorf("summary: no SUMMARY entries in response")
	}
	s := summary.SUMMARY[0]

	// "MHS av" is the since-boot average. It tracks the device's own
	// GHSavg/MGHS fields (from "stats", below) closely; "MHS 5s" is far
	// noisier on a single/few-chip device and was observed swinging to 2x
	// the settled value over a 5-second window, so it is deliberately not
	// used here.
	if mhs, ok := numField(s, "MHS av"); ok {
		stats.HashrateGHs = mhs / 1000
	}
	if v, ok := numField(s, "Accepted"); ok {
		stats.SharesAccepted = int64(v)
	}
	if v, ok := numField(s, "Rejected"); ok {
		stats.SharesRejected = int64(v)
	}
	if v, ok := numField(s, "Elapsed"); ok {
		stats.UptimeS = int64(v)
	}
	if v, ok := numField(s, "Found Blocks"); ok {
		stats.BlocksFound = int64(v)
	}

	// Temperature and fan data are NOT in "summary" - they only appear
	// packed into a single freeform string (e.g. "...Temp[32] ... Fan1[1710]
	// FanR[25%]...") inside "stats"'s per-module entry, a well-known
	// cgminer/Avalon quirk. A failure here still returns the summary data
	// gathered above rather than discarding it.
	if statsBody, err := c.command(ctx, "stats"); err == nil {
		stats.Raw += "\n" + string(statsBody)
		parseModuleStats(statsBody, &stats)
	}

	return stats, nil
}

func (c *Client) Restart(ctx context.Context) error                  { return ErrUnsupported }
func (c *Client) SetFan(ctx context.Context, percent int) error      { return ErrUnsupported }
func (c *Client) SetAutoFan(ctx context.Context, enabled bool) error { return ErrUnsupported }

// bracketToken matches the "Key[value]" tokens cgminer/Avalon pack into a
// single STATS string field, e.g. "Temp[32] OTemp[38] ... Fan1[1710]
// FanR[25%]". Values may contain spaces (e.g. "NETFAIL[0 0 0 0 0 0 0 0]"),
// so this only assumes no nested brackets, not no spaces.
var bracketToken = regexp.MustCompile(`([A-Za-z][A-Za-z0-9_]*)\[([^\[\]]*)\]`)

// parseModuleStats extracts Temp/Fan fields from the "MM ID<n>" freeform
// string in a "stats" response and fills them into stats. It intentionally
// does not error on missing fields - this string's exact key set is
// undocumented and may vary by firmware/model.
func parseModuleStats(body []byte, stats *Stats) {
	var parsed struct {
		STATS []map[string]json.RawMessage `json:"STATS"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return
	}

	var moduleStr string
	for _, entry := range parsed.STATS {
		for key, raw := range entry {
			if !strings.HasPrefix(key, "MM ID") {
				continue
			}
			var s string
			if json.Unmarshal(raw, &s) == nil {
				moduleStr = s
			}
		}
		if moduleStr != "" {
			break
		}
	}
	if moduleStr == "" {
		return
	}

	fields := make(map[string]string)
	for _, m := range bracketToken.FindAllStringSubmatch(moduleStr, -1) {
		fields[m[1]] = strings.TrimSpace(m[2])
	}

	// TAvg (average ASIC temperature) is the comparable figure to AxeOS's
	// "temp" field. Temp/OTemp in this same string read much lower and
	// appear to be board/ambient/outlet sensors, not chip temperature.
	if v, ok := parseFloat(fields["TAvg"]); ok {
		stats.TempC = v
	}
	if v, ok := parseInt(fields["Fan1"]); ok {
		stats.FanRPM = v
	}
	if pct, ok := fields["FanR"]; ok {
		if v, ok := parseFloat(strings.TrimSuffix(pct, "%")); ok {
			stats.FanPercent = v
		}
	}
}

func parseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func parseInt(s string) (int, bool) {
	v, err := strconv.Atoi(s)
	return v, err == nil
}

func (c *Client) command(ctx context.Context, cmd string) ([]byte, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.addr, err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(c.timeout))
	}

	payload, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(payload); err != nil {
		return nil, fmt.Errorf("write command: %w", err)
	}

	// cgminer closes the connection after writing one response; a NUL byte
	// commonly terminates it too. Read to EOF and trim any trailing NULs.
	reader := bufio.NewReader(conn)
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}
	for len(buf) > 0 && buf[len(buf)-1] == 0 {
		buf = buf[:len(buf)-1]
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("empty response from %s", c.addr)
	}
	return buf, nil
}

func numField(m map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return f, true
}
